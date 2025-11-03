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
	httpAddr      = ":8080"
	startupParsed []Entry
)

type Config struct {
	TargetURL string `json:"targetURL"`
}

type Entry struct {
	TimeGMT   string `json:"time_gmt"`   // "(HH:MM GMT)" as on page
	TimeLocal string `json:"time_local"` // Europe/Zurich (e.g. "2025-11-03 10:30 CET")
	Title     string `json:"title"`
	Text      string `json:"text"` // concatenated paragraphs
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

	// Parse once on startup and print JSON to stdout (CLI-friendly)
	entries, err := parseLive(globalTarget)
	if err != nil {
		log.Fatalf("parse error: %v", err)
	}
	startupParsed = entries

	// print to stdout as JSON
	if err := json.NewEncoder(os.Stdout).Encode(entries); err != nil {
		log.Printf("stdout JSON encode error: %v", err)
	}

	// HTTP endpoint for JSON
	http.HandleFunc("/live", handleLiveJSON)
	log.Printf("Serving JSON on %s GET /live (source: %s)", httpAddr, globalTarget)
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
	// fetch fresh each request; if it fails, fall back to startup snapshot
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
	selection := doc.Find(".l-col.l-col--8.live-blog--feed").First()
	if selection.Length() == 0 {
		// graceful fallback if class changes
		selection = doc.Find("main, article, body").First()
	}

	var cur *partial
	selection.Find("*").Each(func(_ int, s *goquery.Selection) {
		tag := goquery.NodeName(s)
		txt := strings.TrimSpace(s.Text())
		if txt == "" {
			return
		}

		// new entry marker?
		if reAbsTime.MatchString(txt) {
			if cur != nil && cur.title != "" {
				entries = append(entries, finalize(*cur))
			}
			cur = &partial{timeGMT: reAbsTime.FindString(txt)}
			return
		}

		if cur == nil {
			return // ignore content before first time marker
		}

		if tag == "h2" && cur.title == "" {
			cur.title = txt
			return
		}
		if tag == "h2" && cur.title != "" {
			// rare case: title appears again without time -> flush current
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

	return entries, nil
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
