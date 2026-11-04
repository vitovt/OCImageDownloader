package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type imageIssue struct {
	Column string `json:"column"`
	URL    string `json:"url"`
	Status int    `json:"http_status,omitempty"`
	Error  string `json:"error"`
}
type rowReport struct {
	Row       int          `json:"row"`
	SEO       string       `json:"seo_url"`
	Main      string       `json:"main"`
	CacheOK   int          `json:"cache_ok"`
	CacheFail int          `json:"cache_fail"`
	Errors    []imageIssue `json:"errors"`
	Warnings  []string     `json:"warnings,omitempty"`
	paths     []string
	success   int
	failed    int
}
type cliOptions struct {
	input, output, url, workdir, report, verify string
	fallback, clean, resume, check, version     bool
	workers, retries, timeout                   int
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	var o cliOptions
	fs := flag.NewFlagSet("ocimagedownloader", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.input, "i", "", "input CSV/XLSX/ODS")
	fs.StringVar(&o.output, "o", "", "output CSV")
	fs.StringVar(&o.url, "u", "", "Google Sheet URL (public CSV export)")
	fs.StringVar(&o.workdir, "d", "", "working directory (default: input directory; URL: current directory)")
	fs.StringVar(&o.report, "report", "", "JSON report path (default: workdir/report.json)")
	fs.StringVar(&o.verify, "verify", "", "verify paths in an output CSV without downloading")
	fs.BoolVar(&o.fallback, "main-fallback", false, "promote first successful cache image when main is missing")
	fs.BoolVar(&o.clean, "clean", false, "delete products before downloading")
	fs.BoolVar(&o.resume, "resume", true, "reuse complete files (default)")
	fs.BoolVar(&o.check, "check-format", false, "report URL extension/content format mismatches")
	fs.BoolVar(&o.version, "version", false, "print version")
	fs.IntVar(&o.workers, "workers", 4, "parallel product workers")
	fs.IntVar(&o.retries, "retries", 3, "additional attempts for network errors, 429, 5xx/6xx")
	fs.IntVar(&o.timeout, "timeout", 30, "request timeout in seconds")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if o.version {
		fmt.Fprintln(stdout, versionText())
		return 0
	}
	if fs.NArg() != 0 || o.workers < 1 || o.retries < 0 || o.timeout < 1 {
		fmt.Fprintln(stderr, "invalid arguments: workers/timeout must be positive and retries nonnegative")
		return 2
	}
	if o.verify != "" {
		return verifyCSV(o, stdout, stderr)
	}
	if (o.input == "") == (o.url == "") || o.output == "" {
		fmt.Fprintln(stderr, "specify exactly one of -i or -u, and -o")
		return 2
	}
	if o.workdir == "" {
		o.workdir = "."
		if o.input != "" {
			o.workdir = filepath.Dir(o.input)
		}
	}
	var records [][]string
	style := csvStyle{delimiter: ','}
	var err error
	globalClient = &http.Client{Timeout: time.Duration(o.timeout) * time.Second}
	if o.input != "" {
		records, style, err = loadCLIInput(o.input)
	} else {
		var endpoint string
		endpoint, err = getCSVURL(o.url)
		if err == nil {
			var resp *http.Response
			resp, err = globalClient.Get(endpoint)
			if err == nil {
				if resp.StatusCode != 200 {
					err = fmt.Errorf("Google Sheet: HTTP %d", resp.StatusCode)
				} else {
					var data []byte
					data, err = io.ReadAll(resp.Body)
					if err == nil {
						records, style, err = parseCSV(data)
					}
				}
				resp.Body.Close()
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	columns, err := validateTable(records)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err = os.MkdirAll(o.workdir, 0755); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if o.clean {
		if err = os.RemoveAll(filepath.Join(o.workdir, "products")); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	good, err := os.Create(filepath.Join(o.workdir, "good.txt"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer good.Close()
	bad, err := os.Create(filepath.Join(o.workdir, "bad.txt"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer bad.Close()
	d := &downloader{client: globalClient, workdir: o.workdir, retries: o.retries, checkFormat: o.check, pause: time.Sleep, force: !o.resume}
	original := make([][]string, len(records))
	for i := range records {
		original[i] = append([]string(nil), records[i]...)
	}
	reports := make([]rowReport, len(records)-1)
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done, okCount, failCount := 0, 0, 0
	for n := 0; n < o.workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				report := processRow(records[i], i, columns, d, o.fallback)
				reports[i-1] = report
				mu.Lock()
				done++
				okCount += report.success
				failCount += report.failed
				for _, p := range report.paths {
					fmt.Fprintln(good, p)
				}
				for _, e := range report.Errors {
					fmt.Fprintf(bad, "row %d %s %s: %s\n", report.Row, e.Column, e.URL, e.Error)
				}
				for _, w := range report.Warnings {
					fmt.Fprintf(stderr, "row %d: %s\n", report.Row, w)
				}
				fmt.Fprintf(stderr, "row %d/%d ok=%d fail=%d\n", done, len(reports), okCount, failCount)
				mu.Unlock()
			}
		}()
	}
	for i := 1; i < len(records); i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err = writeCSV(o.output, records, original, style); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	for _, col := range []string{"main_image", "image_cache"} {
		err = atomicWrite(filepath.Join(o.workdir, col+".txt"), func(w io.Writer) error {
			for _, r := range records[1:] {
				if _, e := fmt.Fprintln(w, r[columns[col]]); e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if o.report == "" {
		o.report = filepath.Join(o.workdir, "report.json")
	}
	err = atomicWrite(o.report, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	missing, fallback := 0, 0
	statuses := map[int]int{}
	extensions := map[string]int{}
	for _, r := range reports {
		if r.Main == "fail" {
			missing++
		}
		if r.Main == "fallback" {
			fallback++
		}
		for _, e := range r.Errors {
			if e.Status != 0 {
				statuses[e.Status]++
			}
		}
		for _, p := range r.paths {
			extensions[filepath.Ext(p)]++
		}
	}
	var codes []int
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	var counts []string
	for _, code := range codes {
		counts = append(counts, fmt.Sprintf("%d:%d", code, statuses[code]))
	}
	fmt.Fprintf(stdout, "rows=%d images=%d/%d fail=%d (%s) main_fallback=%d main_missing=%d\n", len(reports), okCount, okCount+failCount, failCount, strings.Join(counts, " "), fallback, missing)
	// Counts include only successful downloads or validated existing files.
	keys := make([]string, 0, len(extensions))
	for ext := range extensions {
		keys = append(keys, ext)
	}
	sort.Strings(keys)
	for _, ext := range keys {
		fmt.Fprintf(stderr, "%s=%d\n", ext, extensions[ext])
	}
	if missing > 0 {
		return 1
	}
	return 0
}

func validateTable(records [][]string) (map[string]int, error) {
	if len(records) < 2 {
		return nil, fmt.Errorf("table needs a header and at least one data row")
	}
	cols := map[string]int{}
	for i, h := range records[0] {
		h = strings.TrimSpace(h)
		if _, exists := cols[h]; exists {
			return nil, fmt.Errorf("duplicate column: %s", h)
		}
		cols[h] = i
	}
	for _, c := range []string{"main_image", "image_cache", "brand_seo_url"} {
		if _, ok := cols[c]; !ok {
			return nil, fmt.Errorf("missing column: %s", c)
		}
	}
	if _, ok := cols["seo_url"]; !ok {
		if i, ok := cols["seo_url_uk"]; ok {
			cols["seo_url"] = i
		} else {
			return nil, fmt.Errorf("missing column: seo_url or seo_url_uk")
		}
	}
	for i, r := range records[1:] {
		if len(r) != len(records[0]) {
			return nil, fmt.Errorf("row %d: expected %d columns, got %d", i+2, len(records[0]), len(r))
		}
	}
	return cols, nil
}
func processRow(row []string, index int, cols map[string]int, d *downloader, fallback bool) rowReport {
	r := rowReport{Row: index + 1, SEO: row[cols["seo_url"]], Main: "fail", Errors: []imageIssue{}}
	brand := row[cols["brand_seo_url"]]
	fetch := func(raw, col, suffix string) string {
		path, warning, err := d.download(raw, brand, r.SEO, suffix)
		if warning != "" {
			r.Warnings = append(r.Warnings, warning)
		}
		if err != nil {
			status := 0
			var de *downloadError
			if errors.As(err, &de) {
				status = de.Status
			}
			r.Errors = append(r.Errors, imageIssue{Column: col, URL: raw, Status: status, Error: err.Error()})
			r.failed++
			return ""
		}
		r.success++
		r.paths = append(r.paths, path)
		return path
	}
	main := strings.TrimSpace(row[cols["main_image"]])
	mainPath := ""
	if main != "" {
		mainPath = fetch(main, "main_image", fmt.Sprintf("m%d", index-1))
	}
	var cache []string
	raw := row[cols["image_cache"]]
	sep := ","
	if strings.Contains(raw, "|") {
		sep = "|"
	}
	for j, u := range strings.Split(raw, sep) {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		p := fetch(u, "image_cache", fmt.Sprintf("i%d_j%d", index-1, j))
		if p != "" {
			cache = append(cache, p)
			r.CacheOK++
		} else {
			r.CacheFail++
		}
	}
	if mainPath != "" {
		r.Main = "ok"
	} else if fallback && len(cache) > 0 {
		mainPath = cache[0]
		cache = cache[1:]
		r.Main = "fallback"
	}
	if r.Main == "fail" && main == "" {
		r.Errors = append(r.Errors, imageIssue{Column: "main_image", Error: "no main image"})
	}
	row[cols["main_image"]] = mainPath
	row[cols["image_cache"]] = strings.Join(cache, "|")
	return r
}
func verifyCSV(o cliOptions, stdout, stderr io.Writer) int {
	rows, _, err := loadCLIInput(o.verify)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cols, err := validateTable(rows)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if o.workdir == "" {
		o.workdir = filepath.Dir(o.verify)
	}
	failed := false
	for i, r := range rows[1:] {
		for _, col := range []string{"main_image", "image_cache"} {
			for _, p := range strings.Split(r[cols[col]], "|") {
				if p == "" && col == "image_cache" {
					continue
				}
				full := filepath.Join(o.workdir, filepath.FromSlash(p))
				st, e := os.Stat(full)
				if p == "" || e != nil || !st.Mode().IsRegular() || st.Size() == 0 {
					fmt.Fprintf(stdout, "row %d %s %s\n", i+2, col, p)
					failed = true
				}
			}
		}
	}
	if failed {
		return 1
	}
	return 0
}
