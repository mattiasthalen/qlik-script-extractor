package extractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
)

// parseViaReaderAt opens path as an *os.File (an io.ReaderAt) and parses it
// through the memory-mapped-friendly code path.
func parseViaReaderAt(t *testing.T, path string) *extractor.QVFData {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got, err := extractor.ParseReaderAt(f, st.Size())
	if err != nil {
		t.Fatalf("ParseReaderAt: %v", err)
	}
	return got
}

func TestParseReaderAt_MatchesParseQVF_Synthetic(t *testing.T) {
	path := makeMultiBlockQVF(t,
		map[string]any{"qScript": "LOAD 1;"},
		map[string]any{
			"qInfo":    map[string]any{"qId": "m1", "qType": "measure"},
			"qMeasure": map[string]any{"qLabel": "Sales", "qDef": "Sum(A)"},
		},
		map[string]any{
			"qInfo": map[string]any{"qId": "s1", "qType": "sheet"},
			"cells": []map[string]any{{"name": "c1", "type": "kpi"}},
		},
		map[string]any{
			"qInfo":         map[string]any{"qId": "c1", "qType": "kpi"},
			"qHyperCubeDef": map[string]any{"qMeasures": []map[string]any{{"qDef": map[string]any{"qDef": "Count(X)"}}}},
		},
	)

	mem, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatal(err)
	}
	ra := parseViaReaderAt(t, path)

	if ra.Script != mem.Script {
		t.Errorf("script mismatch: %q vs %q", ra.Script, mem.Script)
	}
	if len(ra.Measures) != len(mem.Measures) {
		t.Errorf("measures: reader=%d mem=%d", len(ra.Measures), len(mem.Measures))
	}
	if len(ra.Sheets) != len(mem.Sheets) || len(mem.Sheets) != 1 {
		t.Fatalf("sheets: reader=%d mem=%d", len(ra.Sheets), len(mem.Sheets))
	}
	if len(ra.Sheets[0].Objects) != 1 || ra.Sheets[0].Objects[0].Type != "kpi" {
		t.Errorf("reader sheet objects wrong: %+v", ra.Sheets[0].Objects)
	}
}

func TestParseReaderAt_MatchesParseQVF_RealFile(t *testing.T) {
	path := filepath.Join("testdata", "fixtures", "integration", "Qlik_Sense_Content_Monitor.qvf")
	if _, err := os.Stat(path); err != nil {
		t.Skip("integration fixture not present")
	}

	mem, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatal(err)
	}
	ra := parseViaReaderAt(t, path)

	if len(ra.Measures) != len(mem.Measures) ||
		len(ra.Dimensions) != len(mem.Dimensions) ||
		len(ra.Variables) != len(mem.Variables) ||
		len(ra.Sheets) != len(mem.Sheets) {
		t.Errorf("counts differ: reader{m=%d d=%d v=%d s=%d} mem{m=%d d=%d v=%d s=%d}",
			len(ra.Measures), len(ra.Dimensions), len(ra.Variables), len(ra.Sheets),
			len(mem.Measures), len(mem.Dimensions), len(mem.Variables), len(mem.Sheets))
	}
	if ra.Script != mem.Script {
		t.Errorf("script mismatch (len reader=%d mem=%d)", len(ra.Script), len(mem.Script))
	}
}
