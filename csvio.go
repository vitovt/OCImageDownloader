package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type csvStyle struct {
	delimiter byte
	raw       [][]string
	bom       bool
	crlf      bool
}

func loadCLIInput(path string) ([][]string, csvStyle, error) {
	style := csvStyle{delimiter: ','}
	if strings.ToLower(filepath.Ext(path)) != ".csv" {
		r, e := loadLocalTable(path)
		if e == nil && len(r) > 0 {
			for i := 1; i < len(r); i++ {
				for len(r[i]) < len(r[0]) {
					r[i] = append(r[i], "")
				}
			}
		}
		return r, style, e
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, style, err
	}
	return parseCSV(data)
}
func parseCSV(data []byte) ([][]string, csvStyle, error) {
	style := csvStyle{delimiter: ',', bom: bytes.HasPrefix(data, []byte{239, 187, 191}), crlf: bytes.Contains(data, []byte("\r\n"))}
	data = bytes.TrimPrefix(data, []byte{239, 187, 191})
	// Detect the delimiter from the first record, ignoring delimiters in quotes.
	quoted := false
	commas, semis := 0, 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '"' {
			if quoted && i+1 < len(data) && data[i+1] == '"' {
				i++
				continue
			}
			quoted = !quoted
		}
		if !quoted {
			if c == '\n' {
				break
			}
			if c == ',' {
				commas++
			}
			if c == ';' {
				semis++
			}
		}
	}
	if semis > commas {
		style.delimiter = ';'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = rune(style.delimiter)
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, style, err
	}
	// Retain original cell quoting for unchanged cells and quote preference for replacements.
	var row []string
	start := 0
	quoted = false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '"' {
			if quoted && i+1 < len(data) && data[i+1] == '"' {
				i++
				continue
			}
			quoted = !quoted
		}
		if !quoted && (c == style.delimiter || c == '\n') {
			end := i
			if c == '\n' && end > start && data[end-1] == '\r' {
				end--
			}
			row = append(row, string(data[start:end]))
			start = i + 1
			if c == '\n' {
				if len(row) > 1 || row[0] != "" {
					style.raw = append(style.raw, row)
				}
				row = nil
			}
		}
	}
	if start < len(data) || len(row) > 0 {
		row = append(row, string(data[start:]))
		style.raw = append(style.raw, row)
	}
	if len(style.raw) != len(records) {
		return nil, style, fmt.Errorf("CSV layout could not be preserved")
	}
	return records, style, nil
}
func writeCSV(path string, records, original [][]string, style csvStyle) error {
	return atomicWrite(path, func(w io.Writer) error {
		if style.bom {
			if _, err := w.Write([]byte{239, 187, 191}); err != nil {
				return err
			}
		}
		newline := "\n"
		if style.crlf {
			newline = "\r\n"
		}
		for i, row := range records {
			cells := make([]string, len(row))
			for j, value := range row {
				raw := ""
				hasRaw := i < len(style.raw) && j < len(style.raw[i])
				if hasRaw {
					raw = style.raw[i][j]
				}
				if hasRaw && i < len(original) && j < len(original[i]) && value == original[i][j] {
					cells[j] = raw
					continue
				}
				if strings.HasPrefix(raw, "\"") || strings.ContainsAny(value, string(style.delimiter)+"\"\r\n") {
					value = "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
				}
				cells[j] = value
			}
			if _, err := io.WriteString(w, strings.Join(cells, string(style.delimiter))+newline); err != nil {
				return err
			}
		}
		return nil
	})
}
func atomicWrite(path string, write func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ocimage-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	err = write(f)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
