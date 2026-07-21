package extractor_test

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"os"
	"testing"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
)

// makeMultiBlockQVF builds a fake .qvf where each payload becomes its own zlib
// block, separated by junk — mirroring how real files scatter blocks.
func makeMultiBlockQVF(t *testing.T, payloads ...map[string]any) string {
	t.Helper()
	var out bytes.Buffer
	for _, p := range payloads {
		junk := make([]byte, 16)
		for i := range junk {
			junk[i] = 0xFF
		}
		out.Write(junk)
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var zbuf bytes.Buffer
		w := zlib.NewWriter(&zbuf)
		_, _ = w.Write(b)
		_ = w.Close()
		out.Write(zbuf.Bytes())
	}
	f, err := os.CreateTemp(t.TempDir(), "*.qvf")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = f.Write(out.Bytes())
	_ = f.Close()
	return f.Name()
}

func TestParseQVF_SheetWithChart(t *testing.T) {
	path := makeMultiBlockQVF(t,
		map[string]any{
			"qInfo":    map[string]any{"qId": "sheet1", "qType": "sheet"},
			"qMetaDef": map[string]any{"title": "Overview", "description": "Landing"},
			"rank":     0,
			"cells": []map[string]any{
				{"name": "chart1", "type": "barchart", "col": 0, "row": 0},
			},
		},
		map[string]any{
			"qInfo":         map[string]any{"qId": "chart1", "qType": "barchart"},
			"visualization": "barchart",
			"qMetaDef":      map[string]any{"title": "Sales by Region"},
			"qHyperCubeDef": map[string]any{
				"qDimensions": []map[string]any{
					{"qLibraryId": "", "qDef": map[string]any{"qFieldDefs": []string{"Region"}, "qFieldLabels": []string{"Region"}}},
				},
				"qMeasures": []map[string]any{
					{"qLibraryId": "", "qDef": map[string]any{"qLabel": "Sales", "qDef": "Sum(Amount)"}},
					{"qLibraryId": "masterMeas1", "qDef": map[string]any{}},
				},
			},
		},
	)

	got, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Sheets) != 1 {
		t.Fatalf("expected 1 sheet, got %d", len(got.Sheets))
	}
	s := got.Sheets[0]
	if s.ID != "sheet1" || s.Title != "Overview" || s.Description != "Landing" {
		t.Errorf("unexpected sheet meta: %+v", s)
	}
	if len(s.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(s.Objects))
	}
	o := s.Objects[0]
	if o.ID != "chart1" || o.Type != "barchart" || o.Title != "Sales by Region" {
		t.Errorf("unexpected object: %+v", o)
	}
	if o.Unclassified {
		t.Errorf("object should be classified")
	}
	if len(o.Dimensions) != 1 || o.Dimensions[0].Def != "Region" {
		t.Errorf("unexpected dimensions: %+v", o.Dimensions)
	}
	if len(o.Measures) != 2 {
		t.Fatalf("expected 2 measures, got %d", len(o.Measures))
	}
	if o.Measures[0].Def != "Sum(Amount)" || o.Measures[0].Label != "Sales" {
		t.Errorf("unexpected inline measure: %+v", o.Measures[0])
	}
	if o.Measures[1].LibraryID != "masterMeas1" {
		t.Errorf("expected master measure ref, got %+v", o.Measures[1])
	}
}

func TestParseQVF_UnclassifiedObject(t *testing.T) {
	// Sheet references an object whose block is absent/unrecognised.
	path := makeMultiBlockQVF(t,
		map[string]any{
			"qInfo":    map[string]any{"qId": "sheet1", "qType": "sheet"},
			"qMetaDef": map[string]any{"title": "S1"},
			"cells": []map[string]any{
				{"name": "ghost", "type": "extension-widget"},
			},
		},
	)
	got, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Sheets) != 1 || len(got.Sheets[0].Objects) != 1 {
		t.Fatalf("expected 1 sheet with 1 object, got %+v", got.Sheets)
	}
	o := got.Sheets[0].Objects[0]
	if !o.Unclassified || o.ID != "ghost" || o.Type != "extension-widget" {
		t.Errorf("expected unclassified widget, got %+v", o)
	}
}

func TestParseQVF_SheetTitleExpression(t *testing.T) {
	path := makeMultiBlockQVF(t,
		map[string]any{
			"qInfo": map[string]any{"qId": "sheet1", "qType": "sheet"},
			"cells": []map[string]any{{"name": "kpi1", "type": "kpi"}},
		},
		map[string]any{
			"qInfo": map[string]any{"qId": "kpi1", "qType": "kpi"},
			"title": map[string]any{
				"qStringExpression": map[string]any{"qExpr": "='Total ' & Year"},
			},
			"qHyperCubeDef": map[string]any{
				"qMeasures": []map[string]any{
					{"qDef": map[string]any{"qLabel": "KPI", "qDef": "Count(Id)"}},
				},
			},
		},
	)
	got, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o := got.Sheets[0].Objects[0]
	if o.Title != "='Total ' & Year" {
		t.Errorf("expected expression title, got %q", o.Title)
	}
	if len(o.Measures) != 1 || o.Measures[0].Def != "Count(Id)" {
		t.Errorf("unexpected measures: %+v", o.Measures)
	}
}

func TestParseQVF_MultipleSheetsSortedByRank(t *testing.T) {
	path := makeMultiBlockQVF(t,
		map[string]any{
			"qInfo":    map[string]any{"qId": "sheetB", "qType": "sheet"},
			"qMetaDef": map[string]any{"title": "B"}, "rank": 2,
		},
		map[string]any{
			"qInfo":    map[string]any{"qId": "sheetA", "qType": "sheet"},
			"qMetaDef": map[string]any{"title": "A"}, "rank": 1,
		},
	)
	got, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Sheets) != 2 {
		t.Fatalf("expected 2 sheets, got %d", len(got.Sheets))
	}
	if got.Sheets[0].ID != "sheetA" || got.Sheets[1].ID != "sheetB" {
		t.Errorf("sheets not sorted by rank: %s, %s", got.Sheets[0].ID, got.Sheets[1].ID)
	}
}

func TestParseQVF_NoSheets_EmptySlice(t *testing.T) {
	path := makeQVFFile(t, map[string]any{"qScript": "LOAD 1;"})
	got, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Sheets == nil {
		t.Error("expected non-nil empty Sheets slice")
	}
}
