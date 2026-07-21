package catalog_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mattiasthalen/qlik-parser/internal/catalog"
	"github.com/mattiasthalen/qlik-parser/internal/extractor"
)

func TestCatalog_DuplicatesAndConflicts(t *testing.T) {
	appA := &extractor.QVFData{
		Measures: []extractor.Measure{
			{ID: "m1", Label: "Sales", Def: "Sum(Amount)"},
			{ID: "m2", Label: "Margin", Def: "Sum(Profit)/Sum(Amount)"},
		},
	}
	appB := &extractor.QVFData{
		Measures: []extractor.Measure{
			{ID: "m3", Label: "Sales", Def: "Sum(Amount)"},           // duplicate, same def
			{ID: "m4", Label: "Margin", Def: "Sum(Profit)/Sum(Rev)"}, // conflict, different def
		},
	}

	b := catalog.NewBuilder()
	b.AddApp("appA.qvf", appA)
	b.AddApp("appB.qvf", appB)
	cat := b.Build()

	if len(cat.Apps) != 2 {
		t.Errorf("expected 2 apps, got %v", cat.Apps)
	}

	sales := findEntry(cat.Measures, "Sales")
	if sales == nil {
		t.Fatal("Sales entry missing")
	}
	if sales.Conflicting {
		t.Errorf("Sales should not conflict (same def in both apps): %+v", sales)
	}
	if len(sales.Definitions) != 1 || len(sales.Definitions[0].Apps) != 2 {
		t.Errorf("Sales should have one def used by two apps: %+v", sales.Definitions)
	}

	margin := findEntry(cat.Measures, "Margin")
	if margin == nil {
		t.Fatal("Margin entry missing")
	}
	if !margin.Conflicting || len(margin.Definitions) != 2 {
		t.Errorf("Margin should conflict with two defs: %+v", margin)
	}

	// Conflicts convenience list should contain Margin but not Sales.
	if findEntry(cat.Conflicts, "Margin") == nil {
		t.Error("Margin missing from Conflicts")
	}
	if findEntry(cat.Conflicts, "Sales") != nil {
		t.Error("Sales should not be in Conflicts")
	}
}

func TestCatalog_DimensionsAndVariables(t *testing.T) {
	app := &extractor.QVFData{
		Dimensions: []extractor.Dimension{
			{ID: "d1", Label: "Region", Fields: []string{"Country", "City"}},
		},
		Variables: []extractor.Variable{
			{ID: "v1", Name: "vThreshold", Value: json.RawMessage("100")},
		},
	}
	b := catalog.NewBuilder()
	b.AddApp("app.qvf", app)
	cat := b.Build()

	region := findEntry(cat.Dimensions, "Region")
	if region == nil || region.Definitions[0].Definition != "Country, City" {
		t.Errorf("unexpected Region dimension: %+v", region)
	}
	v := findEntry(cat.Variables, "vThreshold")
	if v == nil || v.Definitions[0].Definition != "100" {
		t.Errorf("unexpected variable: %+v", v)
	}
}

func TestCatalog_NDJSON(t *testing.T) {
	app := &extractor.QVFData{
		Measures: []extractor.Measure{{ID: "m1", Label: "Sales", Def: "Sum(Amount)"}},
	}
	b := catalog.NewBuilder()
	b.AddApp("app.qvf", app)
	nd, err := b.Build().NDJSON()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(nd)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 NDJSON row, got %d: %q", len(lines), nd)
	}
	var row catalog.FlatRow
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("row not valid JSON: %v", err)
	}
	if row.Name != "Sales" || row.App != "app.qvf" || row.Kind != "measure" {
		t.Errorf("unexpected row: %+v", row)
	}
}

func TestCatalog_LabellessFallsBackToID(t *testing.T) {
	app := &extractor.QVFData{
		Measures: []extractor.Measure{{ID: "abc-123", Label: "", Def: "Count(X)"}},
	}
	b := catalog.NewBuilder()
	b.AddApp("app.qvf", app)
	cat := b.Build()
	if findEntry(cat.Measures, "abc-123") == nil {
		t.Errorf("expected id fallback name, got %+v", cat.Measures)
	}
}

func findEntry(entries []catalog.Entry, name string) *catalog.Entry {
	for i := range entries {
		if entries[i].Name == name {
			return &entries[i]
		}
	}
	return nil
}
