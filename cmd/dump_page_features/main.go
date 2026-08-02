// Command dump_page_features joins a completed stage-1 labelling sheet to the
// numeric features the parser sees, so the page gate can be fitted rather than
// guessed at.
//
// The sheet carries a human answer to "does this page list openings" and the
// archive carries the page. This recomputes careerparser.Candidates for each
// labelled page and writes one row per page: every feature of its best
// candidate group, plus the label.
//
// Input:  page_labels*.csv (labelled) + html_store/
// Output: page_features.csv
//
// Env:
//
//	LABELS      completed stage-1 sheet (default page_labels_pe.csv)
//	STORE_DIR   archive directory (default html_store)
//	OUTPUT      dataset path (default page_features.csv)
package main

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/careerscout/careerscout/internal/careerparser"
)

var depthKeyRe = regexp.MustCompile(`^(.*)__l(\d+)_(\d+)$`)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func depthOf(key string) int {
	if m := depthKeyRe.FindStringSubmatch(key); m != nil {
		d, _ := strconv.Atoi(m[2])
		return d
	}
	return 1
}

func readGz(path string) (html, pageURL string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", "", false
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		return "", "", false
	}
	return string(b), zr.Comment, true
}

func main() {
	labels := env("LABELS", "page_labels_pe.csv")
	storeDir := env("STORE_DIR", "html_store")
	outPath := env("OUTPUT", "page_features.csv")

	f, err := os.Open(labels)
	if err != nil {
		log.Fatalf("open %s: %v", labels, err)
	}
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	f.Close()
	if err != nil || len(recs) < 2 {
		log.Fatalf("read %s: %v", labels, err)
	}
	idx := map[string]int{}
	for i, h := range recs[0] {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	get := func(rec []string, k string) string {
		if i, ok := idx[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	out, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	w := csv.NewWriter(out)
	defer w.Flush()
	_ = w.Write([]string{"domain", "depth", "label",
		"n_groups", "n_members", "link_ratio", "cohesion", "cohesion_deep",
		"median_words", "median_text_len", "loc_ratio", "date_ratio",
		"distinct_ratio", "vocab_in_sig", "job_heading_near", "depth_from_body",
		"heuristic_score", "extracted"})

	var n, skipped int
	for _, rec := range recs[1:] {
		lab := get(rec, "label")
		if lab != "0" && lab != "1" {
			continue
		}
		dom := get(rec, "domain")
		html, pageURL, ok := readGz(filepath.Join(storeDir, dom+".html.gz"))
		if !ok {
			skipped++
			continue
		}
		// Every group, not just the winner: a page whose best group is weak but
		// whose second is strong fails differently from one with no groups at
		// all, and only the ranked list distinguishes them.
		cands := careerparser.Candidates(html, pageURL, 0, 0)
		row := []string{dom, strconv.Itoa(depthOf(dom)), lab, strconv.Itoa(len(cands))}
		if len(cands) == 0 {
			row = append(row, "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0")
		} else {
			ft := cands[0].Feat
			row = append(row,
				strconv.Itoa(ft.NMembers),
				fmt.Sprintf("%.4f", ft.LinkRatio),
				fmt.Sprintf("%.4f", ft.Cohesion),
				strconv.Itoa(ft.CohesionDeep),
				strconv.Itoa(ft.MedianWords),
				strconv.Itoa(ft.MedianTextLen),
				fmt.Sprintf("%.4f", ft.LocRatio),
				fmt.Sprintf("%.4f", ft.DateRatio),
				fmt.Sprintf("%.4f", ft.DistinctRatio),
				strconv.Itoa(ft.VocabInSig),
				strconv.Itoa(ft.JobHeadingNr),
				strconv.Itoa(ft.DepthFromBody),
				fmt.Sprintf("%.4f", ft.HeuristicScr))
		}
		ex := "0"
		if len(careerparser.Extract(html, pageURL)) > 0 {
			ex = "1"
		}
		row = append(row, ex)
		_ = w.Write(row)
		n++
	}
	log.Printf("DONE | %d labelled pages featurised | %d missing from the archive -> %s",
		n, skipped, outPath)
}
