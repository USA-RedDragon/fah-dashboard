// Package roster reads the event sign-up sheet.
package roster

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	nameColumn = "LTT Forum Name"
	fahColumn  = "F@H VALIDATION"
	validValue = "VALID"
)

// ErrBadSheet is returned when the sheet does not have the expected columns.
var ErrBadSheet = errors.New("sheet is missing an expected column")

// Parse returns the unique names in the sheet. A sheet with an F@H validation
// column keeps only the names marked VALID.
func Parse(r io.Reader) ([]string, error) {
	rows, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read sheet: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrBadSheet
	}

	nameIdx, fahIdx := -1, -1
	for i, column := range rows[0] {
		switch strings.TrimSpace(column) {
		case nameColumn:
			nameIdx = i
		case fahColumn:
			fahIdx = i
		}
	}
	if nameIdx < 0 {
		return nil, ErrBadSheet
	}

	seen := map[string]bool{}
	names := make([]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if nameIdx >= len(row) {
			continue
		}
		name := strings.TrimSpace(row[nameIdx])
		key := strings.ToLower(name)
		if name == "" || seen[key] || (fahIdx >= 0 && strings.TrimSpace(row[fahIdx]) != validValue) {
			continue
		}
		seen[key] = true
		names = append(names, name)
	}
	return names, nil
}

// FetchAll reads every sheet and returns the names in any of them.
func FetchAll(ctx context.Context, client *http.Client, urls []string) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	for _, url := range urls {
		got, err := Fetch(ctx, client, url)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", url, err)
		}
		for _, name := range got {
			if key := strings.ToLower(name); !seen[key] {
				seen[key] = true
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// Fetch downloads and parses the sheet at url.
func Fetch(ctx context.Context, client *http.Client, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get sheet: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get sheet: unexpected status %s", resp.Status)
	}
	return Parse(resp.Body)
}
