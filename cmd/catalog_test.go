package cmd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattiasthalen/qlik-parser/cmd"
)

func TestCatalogCmd_WritesJSON(t *testing.T) {
	src := filepath.Join("..", "internal", "extractor", "testdata", "fixtures", "integration")
	if _, err := os.Stat(filepath.Join(src, "Qlik_Sense_Content_Monitor.qvf")); err != nil {
		t.Skip("integration fixture not present")
	}

	outFile := filepath.Join(t.TempDir(), "catalog.json")
	root := cmd.NewRootCmd()
	root.SetArgs([]string{"catalog", "--source", src, "--out", outFile})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("catalog failed: %v", err)
	}

	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading catalog output: %v", err)
	}
	var out struct {
		Apps       []string `json:"apps"`
		Measures   []any    `json:"measures"`
		Dimensions []any    `json:"dimensions"`
		Variables  []any    `json:"variables"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("catalog output not valid JSON: %v", err)
	}
	if len(out.Apps) == 0 {
		t.Error("expected at least one app in catalog")
	}
	if len(out.Measures) == 0 || len(out.Dimensions) == 0 {
		t.Errorf("expected measures and dimensions, got %d/%d", len(out.Measures), len(out.Dimensions))
	}
}

func TestCatalogCmd_SourceNotDir(t *testing.T) {
	root := cmd.NewRootCmd()
	root.SetArgs([]string{"catalog", "--source", "/definitely/not/a/dir/xyz"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for missing source dir")
	}
}
