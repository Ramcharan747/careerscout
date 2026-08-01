// Command fetch_career_html archives the raw HTML of every career page found
// by cmd/career_finder.
//
// Discovery and extraction are deliberately separate passes. The crawler cannot
// be improved mid-run without redoing the whole sweep, but the parser can be
// iterated indefinitely against an archive. Storing the HTML once means every
// later parser change is a local re-run rather than another 8k-domain crawl.
//
// Input:  career_pages.csv  (from cmd/career_finder)
// Output: html_store/<domain>.html.gz, one gzipped page per company
//
//	fetch_html_checkpoint.json for resume
//
// Env:
//
//	LIMIT       process at most N pages
//	WORKERS     concurrent fetchers (default 60)
//	TIMEOUT_MS  per-request timeout (default 15000)
//	STORE_DIR   archive directory (default html_store)
package main

import (
	"compress/gzip"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxBody = 6 << 20 // 6 MB; career pages far exceeding this are asset dumps

type target struct {
	Company string
	Domain  string
	URL     string
	ATS     string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}

func loadCheckpoint(path string) map[string]bool {
	done := map[string]bool{}
	b, err := os.ReadFile(path)
	if err != nil {
		return done
	}
	var keys []string
	if json.Unmarshal(b, &keys) == nil {
		for _, k := range keys {
			done[k] = true
		}
	}
	return done
}

func saveCheckpoint(path string, done map[string]bool) {
	keys := make([]string, 0, len(done))
	for k := range done {
		keys = append(keys, k)
	}
	if b, err := json.Marshal(keys); err == nil {
		_ = os.WriteFile(path, b, 0644)
	}
}

func readTargets(path string) ([]target, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := idx[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	var out []target
	seen := map[string]bool{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		u := get(rec, "career_url")
		d := get(rec, "domain")
		if u == "" || d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, target{
			Company: get(rec, "company_name"),
			Domain:  d,
			URL:     u,
			ATS:     get(rec, "ats_platform"),
		})
	}
	return out, nil
}

// safeName keeps the archive flat and filesystem-safe: one file per domain.
func safeName(domain string) string {
	s := strings.ToLower(domain)
	for _, bad := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|", " "} {
		s = strings.ReplaceAll(s, bad, "_")
	}
	return s
}

func main() {
	var (
		in         = env("INPUT", "career_pages.csv")
		storeDir   = env("STORE_DIR", "html_store")
		ckptPath   = "fetch_html_checkpoint.json"
		workers    = envInt("WORKERS", 60)
		timeoutMS  = envInt("TIMEOUT_MS", 15000)
		limit      = envInt("LIMIT", 0)
		fetched    int64
		skipped    int64
		failed     int64
		bytesTotal int64
	)

	targets, err := readTargets(in)
	if err != nil {
		log.Fatalf("read %s: %v", in, err)
	}
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		log.Fatalf("mkdir %s: %v", storeDir, err)
	}

	done := loadCheckpoint(ckptPath)
	var todo []target
	for _, t := range targets {
		if !done[t.Domain] {
			todo = append(todo, t)
		}
	}
	log.Printf("career pages: %d | already archived: %d | to fetch: %d", len(targets), len(done), len(todo))
	if limit > 0 && limit < len(todo) {
		log.Printf("LIMIT=%d", limit)
		todo = todo[:limit]
	}
	if len(todo) == 0 {
		log.Println("nothing to do")
		return
	}

	client := &http.Client{
		Timeout: time.Duration(timeoutMS) * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			MaxIdleConnsPerHost: 4,
			DisableCompression:  false,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	var mu sync.Mutex
	ch := make(chan target)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range ch {
				req, err := http.NewRequest("GET", t.URL, nil)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					continue
				}
				req.Header.Set("User-Agent",
					"Mozilla/5.0 (compatible; CareerScout/2.1; +https://github.com/Ramcharan747/careerscout)")
				req.Header.Set("Accept", "text/html,application/xhtml+xml")

				resp, err := client.Do(req)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					mu.Lock()
					done[t.Domain] = true
					mu.Unlock()
					continue
				}
				body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
				resp.Body.Close()

				if rerr != nil || resp.StatusCode != http.StatusOK || len(body) < 512 {
					atomic.AddInt64(&skipped, 1)
					mu.Lock()
					done[t.Domain] = true
					mu.Unlock()
					continue
				}

				out := filepath.Join(storeDir, safeName(t.Domain)+".html.gz")
				f, err := os.Create(out)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					continue
				}
				zw := gzip.NewWriter(f)
				// Provenance travels with the archive so the parser can resolve
				// relative links without re-reading the discovery CSV.
				zw.Comment = t.URL
				zw.Name = t.Domain
				_, _ = zw.Write(body)
				_ = zw.Close()
				_ = f.Close()

				atomic.AddInt64(&fetched, 1)
				atomic.AddInt64(&bytesTotal, int64(len(body)))
				mu.Lock()
				done[t.Domain] = true
				mu.Unlock()
			}
		}()
	}

	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(20 * time.Second)
		defer tk.Stop()
		start := time.Now()
		for {
			select {
			case <-tk.C:
				f := atomic.LoadInt64(&fetched)
				el := time.Since(start).Seconds()
				log.Printf("fetched %d | skipped %d | failed %d | %.1f/s | %.0f MB",
					f, atomic.LoadInt64(&skipped), atomic.LoadInt64(&failed),
					float64(f)/el, float64(atomic.LoadInt64(&bytesTotal))/1e6)
				mu.Lock()
				saveCheckpoint(ckptPath, done)
				mu.Unlock()
			case <-stop:
				return
			}
		}
	}()

	for _, t := range todo {
		ch <- t
	}
	close(ch)
	wg.Wait()
	close(stop)

	saveCheckpoint(ckptPath, done)
	log.Printf("DONE | fetched %d | skipped %d | failed %d | %.0f MB archived in %s",
		fetched, skipped, failed, float64(bytesTotal)/1e6, storeDir)
}
