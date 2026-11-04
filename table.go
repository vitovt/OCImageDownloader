package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/xuri/excelize/v2"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func getCSVURL(spreadsheetURL string) (string, error) {
	u, err := url.Parse(spreadsheetURL)
	if err != nil {
		return "", err
	}

	parts := strings.Split(u.Path, "/")
	var spreadsheetID string
	for i, part := range parts {
		if part == "d" && i+1 < len(parts) {
			spreadsheetID = parts[i+1]
			break
		}
	}
	if spreadsheetID == "" {
		return "", errors.New("Invalid Google Spreadsheet URL")
	}

	q := u.Query()
	gid := q.Get("gid")
	if gid == "" {
		if u.Fragment != "" {
			fragParts := strings.Split(u.Fragment, "=")
			if len(fragParts) == 2 && fragParts[0] == "gid" {
				gid = fragParts[1]
			}
		}
		if gid == "" {
			gid = "0"
		}
	}

	csvURL := fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s/export?format=csv&gid=%s", spreadsheetID, gid)
	return csvURL, nil
}

func fetchCSV(csvURL string) ([][]string, error) {
	req, err := http.NewRequest("GET", csvURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := globalClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Failed to fetch CSV data: %s", resp.Status)
	}

	reader := csv.NewReader(resp.Body)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	return records, nil
}

/* ========================= Local table loaders ========================= */

func loadLocalTable(path string) ([][]string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return readCSVFile(path)
	case ".xlsx":
		return readXLSXFile(path)
	case ".ods":
		return readODSFile(path)
	default:
		return nil, fmt.Errorf("unsupported file type: %s (use .csv, .xlsx, or .ods)", filepath.Ext(path))
	}
}

func readCSVFile(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Peek first line to detect delimiter , vs ;
	br := bufio.NewReader(f)
	firstLine, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}

	comma := ','
	if strings.Count(firstLine, ";") > strings.Count(firstLine, ",") {
		comma = ';'
	}

	// Stitch the first line back to the reader
	r := csv.NewReader(io.MultiReader(strings.NewReader(firstLine), br))
	r.Comma = comma

	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	return trimEmptyRows(records), nil
}

func readXLSXFile(path string) ([][]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("xlsx has no sheets")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	return trimEmptyRows(rows), nil
}

/* ------------------------- ODS reader ------------------------- */
func readODSFile(path string) ([][]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open ods (zip): %w", err)
	}
	defer zr.Close()

	var content io.ReadCloser
	for _, zf := range zr.File {
		if zf.Name == "content.xml" {
			rc, err := zf.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open content.xml: %w", err)
			}
			content = rc
			break
		}
	}
	if content == nil {
		return nil, errors.New("content.xml not found in ODS")
	}
	defer content.Close()

	dec := xml.NewDecoder(content)
	var (
		inTable   bool
		inRow     bool
		rows      [][]string
		curRow    []string
		tableDone bool
	)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml decode error: %w", err)
		}

		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "table": // <table:table>
				if !inTable {
					inTable = true
				}
			case "table-row": // <table:table-row>
				if inTable {
					inRow = true
					curRow = []string{}
				}
			case "table-cell": // <table:table-cell>
				if inTable && inRow {
					repeat := parseRepeatAttr(se.Attr)
					val, err := readODSCellText(dec)
					if err != nil {
						return nil, err
					}
					val = strings.TrimSpace(val)
					for i := 0; i < repeat; i++ {
						curRow = append(curRow, val)
					}
				}
			case "covered-table-cell": // merged cells continuation
				if inTable && inRow {
					repeat := parseRepeatAttr(se.Attr)
					for i := 0; i < repeat; i++ {
						curRow = append(curRow, "")
					}
				}
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "table-row":
				if inTable && inRow {
					rows = append(rows, curRow)
					inRow = false
				}
			case "table":
				if inTable {
					inTable = false
					tableDone = true
				}
			}
		}
		if tableDone {
			break // Only first table
		}
	}

	if len(rows) == 0 {
		return nil, errors.New("no table rows found in ODS")
	}
	return trimEmptyRows(rows), nil
}

func parseRepeatAttr(attrs []xml.Attr) int {
	repeat := 1
	for _, a := range attrs {
		if a.Name.Local == "number-columns-repeated" {
			if n, err := strconv.Atoi(a.Value); err == nil && n > 1 {
				repeat = n
			}
		}
	}
	return repeat
}

// readODSCellText consumes tokens inside the current <table:table-cell>
// and concatenates all textual content, handling text:s (spaces) and line breaks.
func readODSCellText(dec *xml.Decoder) (string, error) {
	var buf bytes.Buffer
	depth := 1 // already inside <table-cell>
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			// handle <text:s text:c="N"/> → N spaces (default 1)
			if t.Name.Local == "s" {
				n := 1
				for _, a := range t.Attr {
					if a.Name.Local == "c" {
						if v, err := strconv.Atoi(a.Value); err == nil && v > 0 {
							n = v
						}
					}
				}
				buf.WriteString(strings.Repeat(" ", n))
			}
			// optional: <text:line-break/>
			if t.Name.Local == "line-break" {
				buf.WriteString("\n")
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			buf.Write(t)
		}
	}
	return buf.String(), nil
}

func trimEmptyRows(rows [][]string) [][]string {
	var out [][]string
	for _, r := range rows {
		keep := false
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				keep = true
				break
			}
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}
