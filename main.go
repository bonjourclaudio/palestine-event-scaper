// go 1.21+
// go get github.com/PuerkitoBio/goquery
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

var (
	reAbsTime     = regexp.MustCompile(`\(\d{2}:\d{2}\s*GMT\)`) // "(HH:MM GMT)"
	europeZurich  *time.Location
	globalTarget  string
	httpAddr      = ":8000"
	startupParsed []Entry
)

type Config struct {
	TargetURL string `json:"targetURL"`
}

type Entry struct {
	TimeGMT   string `json:"time_gmt"`
	TimeLocal string `json:"time_local"`
	Title     string `json:"title"`
	Text      string `json:"text"`
}

type partial struct {
	timeGMT string
	title   string
	texts   []string
}

func main() {
	var err error
	europeZurich, err = time.LoadLocation("Europe/Zurich")
	if err != nil {
		log.Fatal(err)
	}

	cfg, err := readConfig("config.json")
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	globalTarget = cfg.TargetURL
	if globalTarget == "" {
		log.Fatal("config.json missing 'targetURL'")
	}

	entries, err := parseLive(globalTarget)
	if err != nil {
		log.Fatalf("parse error: %v", err)
	}
	startupParsed = entries

	if err := json.NewEncoder(os.Stdout).Encode(entries); err != nil {
		log.Printf("stdout JSON encode error: %v", err)
	}

	// HTTP endpoint for JSON (with CORS)
	http.Handle("/getRecentEvents", withCORS(http.HandlerFunc(handleLiveJSON)))

	log.Printf("Serving JSON on %s GET /getRecentEvents (source: %s)", httpAddr, globalTarget)
	log.Fatal(http.ListenAndServe(httpAddr, nil))
}

func readConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func handleLiveJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		// Preflight CORS check
		w.WriteHeader(http.StatusOK)
		return
	}

	entries, err := parseLive(globalTarget)
	if err != nil {
		log.Printf("refresh failed, serving startup snapshot: %v", err)
		entries = startupParsed
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(entries); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
}

// --- CORS middleware ---
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- parsing logic ---
func parseLive(url string) ([]Entry, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StableNewsScraper/1.0)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, 64)

	// ✅ Restrict to the live feed container
	selection := doc.Find(".l-col.l-col--8.live-blog--feed").First()
	if selection.Length() == 0 {
		selection = doc.Find("main, article, body").First() // fallback
	}

	var cur *partial
	selection.Find("*").Each(func(_ int, s *goquery.Selection) {
		tag := goquery.NodeName(s)
		txt := strings.TrimSpace(s.Text())
		if txt == "" {
			return
		}

		if reAbsTime.MatchString(txt) {
			if cur != nil && cur.title != "" {
				entries = append(entries, finalize(*cur))
			}
			cur = &partial{timeGMT: reAbsTime.FindString(txt)}
			return
		}

		if cur == nil {
			return
		}

		if tag == "h2" && cur.title == "" {
			cur.title = txt
			return
		}
		if tag == "h2" && cur.title != "" {
			entries = append(entries, finalize(*cur))
			cur = &partial{title: txt}
			return
		}

		if tag == "p" || tag == "li" {
			cur.texts = append(cur.texts, txt)
		}
	})

	if cur != nil && cur.title != "" {
		entries = append(entries, finalize(*cur))
	}

	// Only keep well-formed entries
	filtered := entries[:0]
	for _, e := range entries {
		if e.TimeGMT != "" && e.Title != "" {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

func finalize(p partial) Entry {
	var local string
	if p.timeGMT != "" {
		s := strings.Trim(p.timeGMT, "()")
		s = strings.TrimSuffix(s, " GMT")
		now := time.Now().UTC()
		if t, err := time.ParseInLocation("15:04", s, time.UTC); err == nil {
			combined := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
			local = combined.In(europeZurich).Format("2006-01-02 15:04 MST")
		}
	}
	return Entry{
		TimeGMT:   p.timeGMT,
		TimeLocal: local,
		Title:     p.title,
		Text:      strings.Join(p.texts, "\n"),
	}
}
