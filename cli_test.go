package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/xuri/excelize/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var pngImage = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)

func TestCLIOutputFallbackAndReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngImage)
	}))
	defer server.Close()
	dir := t.TempDir()
	input := filepath.Join(dir, "in.csv")
	output := filepath.Join(dir, "out.csv")
	source := fmt.Sprintf("\ufeff\"seo_url\";brand_seo_url;main_image;image_cache;description\r\np1;brand;\"%s/missing\";\"%s/photo.jpg?v=2|%s/missing\";\"hello; world\"\r\np2;brand;%s/photo;;\"two\nlines\"\r\n", server.URL, server.URL, server.URL, server.URL)
	os.WriteFile(input, []byte(source), 0644)
	var out, stderr bytes.Buffer
	code := runCLI([]string{"-i", input, "-o", output, "--main-fallback", "--check-format", "--workers", "2"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("\"hello; world\"")) || !bytes.HasPrefix(data, []byte("\ufeff\"seo_url\";")) {
		t.Fatalf("lost CSV style: %s", data)
	}
	rows, _, err := loadCLIInput(output)
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][2] != "products/brand/p1_i0_j0.png" || rows[1][3] != "" || rows[2][4] != "two\nlines" {
		t.Fatalf("rows: %#v", rows)
	}
	if !strings.Contains(stderr.String(), "format mismatch") || !strings.Contains(out.String(), "main_fallback=1 main_missing=0") {
		t.Fatalf("output: %s %s", out.String(), stderr.String())
	}
	reportData, _ := os.ReadFile(filepath.Join(dir, "report.json"))
	var reports []rowReport
	if err = json.Unmarshal(reportData, &reports); err != nil || len(reports) != 2 || reports[0].Main != "fallback" || reports[0].Errors[0].Status != 404 {
		t.Fatalf("report: %s %v", reportData, err)
	}
	if code = runCLI([]string{"--verify", output}, &out, &stderr); code != 0 {
		t.Fatalf("verify=%d %s", code, out.String())
	}
}
func TestMissingMainAndInvalidInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.csv")
	output := filepath.Join(dir, "out.csv")
	os.WriteFile(input, []byte("seo_url,brand_seo_url,main_image,image_cache\np,b,invalid,\n"), 0644)
	var out bytes.Buffer
	if code := runCLI([]string{"-i", input, "-o", output}, &out, &out); code != 1 {
		t.Fatalf("code=%d %s", code, out.String())
	}
	rows, _, _ := loadCLIInput(output)
	if rows[1][2] != "" {
		t.Fatal("failed main retained")
	}
	os.WriteFile(input, []byte("wrong,header\nx,y\n"), 0644)
	if code := runCLI([]string{"-i", input, "-o", output, "--clean"}, &out, &out); code != 2 {
		t.Fatalf("code=%d", code)
	}
}
func TestDownloadRetriesAndAtomicResume(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(pngImage)))
		w.Write(pngImage)
	}))
	defer server.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "products", "b", "p_m0.png")
	os.MkdirAll(filepath.Dir(target), 0755)
	os.WriteFile(target, nil, 0644)
	var delays []time.Duration
	d := downloader{client: server.Client(), workdir: dir, retries: 3, pause: func(v time.Duration) { delays = append(delays, v) }}
	path, _, err := d.download(server.URL+"/image.jpg?v=2", "b", "p", "m0")
	if err != nil || path != "products/b/p_m0.png" || calls.Load() != 2 || len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("%s %v calls=%d delays=%v", path, err, calls.Load(), delays)
	}
	st, _ := os.Stat(target)
	if st.Size() != int64(len(pngImage)) {
		t.Fatal("zero file not replaced")
	}
	os.WriteFile(target, pngImage[:10], 0644)
	if _, _, err = d.download(server.URL, "b", "p", "m0"); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(target)
	if st.Size() != int64(len(pngImage)) {
		t.Fatal("truncated file not replaced")
	}
	if _, err = os.Stat(target + ".part"); !os.IsNotExist(err) {
		t.Fatal("part file remains")
	}
	// Resume must leave a complete existing file untouched.
	old := time.Unix(1234567890, 0)
	os.Chtimes(target, old, old)
	d.download(server.URL, "b", "p", "m0")
	st, _ = os.Stat(target)
	if !st.ModTime().Equal(old) {
		t.Fatal("complete file was rewritten")
	}
}
func TestTruncatedResponseAnd404(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/404" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", "999")
		w.Write(pngImage)
	}))
	defer server.Close()
	dir := t.TempDir()
	d := downloader{client: server.Client(), workdir: dir, retries: 1, pause: func(time.Duration) {}}
	if _, _, err := d.download(server.URL, "b", "p", "m0"); err == nil {
		t.Fatal("accepted truncated response")
	}
	if calls.Load() != 2 {
		t.Fatal("network error not retried")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "products", "b", "*"))
	if len(files) != 0 {
		t.Fatalf("partial files: %v", files)
	}
	calls.Store(0)
	d.download(server.URL+"/404", "b", "p", "m0")
	if calls.Load() != 1 {
		t.Fatal("404 retried")
	}
}
func TestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	d := downloader{client: &http.Client{Timeout: 20 * time.Millisecond}, workdir: t.TempDir(), pause: func(time.Duration) {}}
	if _, _, err := d.download(server.URL, "b", "p", "m0"); err == nil {
		t.Fatal("timeout ignored")
	}
}

func TestLocalWorkbookInputs(t *testing.T) {
	dir := t.TempDir()
	workbook := excelize.NewFile()
	workbook.SetSheetRow("Sheet1", "A1", &[]string{"seo_url", "brand_seo_url", "main_image", "image_cache"})
	workbook.SetSheetRow("Sheet1", "A2", &[]string{"p", "b"})
	xlsx := filepath.Join(dir, "in.xlsx")
	if err := workbook.SaveAs(xlsx); err != nil {
		t.Fatal(err)
	}
	workbook.Close()
	ods := filepath.Join(dir, "in.ods")
	f, err := os.Create(ods)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	entry, err := z.Create("content.xml")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(entry, `<document><table><table-row><table-cell><p>seo_url</p></table-cell><table-cell><p>brand_seo_url</p></table-cell><table-cell><p>main_image</p></table-cell><table-cell><p>image_cache</p></table-cell></table-row><table-row><table-cell><p>p</p></table-cell><table-cell><p>b</p></table-cell><table-cell number-columns-repeated="2"/></table-row></table></document>`)
	z.Close()
	f.Close()
	for _, path := range []string{xlsx, ods} {
		rows, _, err := loadCLIInput(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = validateTable(rows); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(rows[1]) != 4 || rows[1][2] != "" {
			t.Fatalf("%#v", rows)
		}
	}
}
