package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type downloadError struct {
	Status int
	Err    error
}

func (e *downloadError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("HTTP %d: %v", e.Status, e.Err)
	}
	return e.Err.Error()
}
func (e *downloadError) Unwrap() error { return e.Err }

type downloader struct {
	client      *http.Client
	workdir     string
	retries     int
	force       bool
	checkFormat bool
	pause       func(time.Duration)
}

func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}
func (d *downloader) download(raw, brand, seo, suffix string) (string, string, error) {
	if !safeName(brand) || !safeName(seo) {
		return "", "", fmt.Errorf("unsafe or empty brand/product name")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("invalid HTTP image URL: %s", raw)
	}
	var path, warning string
	for attempt := 0; attempt <= d.retries; attempt++ {
		path, warning, err = d.attempt(u, brand, seo, suffix)
		if err == nil {
			return path, warning, nil
		}
		var de *downloadError
		if !errors.As(err, &de) || (de.Status != 0 && de.Status != 429 && (de.Status < 500 || de.Status >= 700)) {
			break
		}
		if attempt < d.retries {
			delays := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
			n := attempt
			if n > 2 {
				n = 2
			}
			d.pause(delays[n])
		}
	}
	return path, warning, err
}

func (d *downloader) attempt(u *url.URL, brand, seo, suffix string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 OCImageDownloader/"+Version)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", "", &downloadError{Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", &downloadError{Status: resp.StatusCode, Err: fmt.Errorf("%s", resp.Status)}
	}
	reader := bufio.NewReader(resp.Body)
	sample, peekErr := reader.Peek(512)
	if peekErr != nil && peekErr != io.EOF && peekErr != bufio.ErrBufferFull {
		return "", "", &downloadError{Err: peekErr}
	}
	if len(sample) == 0 {
		return "", "", &downloadError{Err: fmt.Errorf("empty response")}
	}
	detected := http.DetectContentType(sample)
	ext := imageExtension(detected)
	if ext == "" {
		ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		ext = imageExtension(ct)
	}
	if ext == "" {
		return "", "", fmt.Errorf("unsupported image content: %s", detected)
	}
	warning := ""
	original := strings.ToLower(filepath.Ext(u.Path))
	if d.checkFormat && original != "" && original != ext && !(ext == ".jpg" && original == ".jpeg") {
		warning = fmt.Sprintf("format mismatch: URL extension %s, detected %s", original, ext)
	}
	relative := filepath.ToSlash(filepath.Join("products", brand, seo+"_"+suffix+ext))
	target := filepath.Join(d.workdir, filepath.FromSlash(relative))
	if st, e := os.Stat(target); !d.force && e == nil && st.Size() > 0 && resp.ContentLength > 0 && st.Size() == resp.ContentLength {
		return relative, warning, nil
	}
	if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", warning, err
	}
	part := target + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", warning, err
	}
	defer os.Remove(part)
	n, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr != nil {
		return "", warning, &downloadError{Err: copyErr}
	}
	if closeErr != nil {
		return "", warning, closeErr
	}
	if n == 0 || (resp.ContentLength >= 0 && n != resp.ContentLength) {
		return "", warning, &downloadError{Err: fmt.Errorf("size mismatch: expected %d, received %d", resp.ContentLength, n)}
	}
	if err = os.Rename(part, target); err != nil {
		return "", warning, err
	}
	return relative, warning, nil
}
func imageExtension(ct string) string {
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	case "image/avif":
		return ".avif"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	}
	return ""
}
