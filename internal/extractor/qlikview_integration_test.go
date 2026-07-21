package extractor_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattiasthalen/qlik-parser/cmd"
	"github.com/mattiasthalen/qlik-parser/internal/extractor"
)

const qlikviewTestdata = "testdata/fixtures/integration"

func skipIfNoQlikviewFixtures(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(qlikviewTestdata); os.IsNotExist(err) {
		t.Skip("real QVW fixtures not present — skipping")
	}
}

func TestQlikview_WalkerFindsAllFiles(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	paths, warns := extractor.Walk(qlikviewTestdata)

	if len(warns) != 0 {
		t.Errorf("expected no warnings, got: %v", warns)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 files (1 QVW + 1 QVF), got %d: %v", len(paths), paths)
	}
}

func TestQlikview_AllFilesExtractWithoutError(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	paths, _ := extractor.Walk(qlikviewTestdata)
	for _, p := range paths {
		rel, _ := filepath.Rel(qlikviewTestdata, p)
		t.Run(rel, func(t *testing.T) {
			var err error
			switch filepath.Ext(p) {
			case ".qvf":
				_, err = extractor.ExtractScriptFromQVF(p)
			default:
				_, err = extractor.ExtractScript(p)
			}
			if err != nil {
				var noScript *extractor.NoScriptError
				if errors.As(err, &noScript) {
					t.Errorf("no script found in %s", rel)
				} else {
					t.Errorf("extraction error for %s: %v", rel, err)
				}
			}
		})
	}
}

func TestQlikview_AllScriptsHaveExpectedContent(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	// Known content anchors per fixture, keyed by file extension.
	// prefix: first distinctive text after ///$tab <tab name>\r\n
	// suffix: last distinctive text at the very end of the script (trimmed)
	type anchor struct {
		prefix string
		suffix string
	}
	anchors := map[string]anchor{
		".qvw": {
			prefix: "///$tab Main\r\n//Copyright",
			suffix: "", // suffix not asserted: fixture contains binary padding after script end
		},
		".qvf": {
			prefix: "///$tab ** about **\r\n/*",
			suffix: "Trace Woohoo! $(reload_message) Rejoice!;",
		},
	}

	paths, _ := extractor.Walk(qlikviewTestdata)
	for _, p := range paths {
		rel, _ := filepath.Rel(qlikviewTestdata, p)
		ext := filepath.Ext(p)
		t.Run(rel, func(t *testing.T) {
			var script string
			var err error
			switch ext {
			case ".qvf":
				script, err = extractor.ExtractScriptFromQVF(p)
			default:
				script, err = extractor.ExtractScript(p)
			}
			if err != nil {
				t.Fatalf("extraction error: %v", err)
			}
			a, ok := anchors[ext]
			if !ok {
				t.Skipf("no anchor defined for extension %s", ext)
			}
			if !strings.HasPrefix(script, a.prefix) {
				t.Errorf("expected script to start with %q, got prefix: %q", a.prefix, script[:min(len(a.prefix)+20, len(script))])
			}
			if a.suffix != "" {
				trimmed := strings.TrimRight(script, "\r\n\t ")
				if !strings.HasSuffix(trimmed, a.suffix) {
					t.Errorf("expected script to end with %q, got suffix: %q", a.suffix, trimmed[max(0, len(trimmed)-len(a.suffix)-20):])
				}
			}
		})
	}
}

func TestQlikview_ExtractDryRun_WritesNoFiles(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	srcDir, _ := filepath.Abs(qlikviewTestdata)
	outDir := t.TempDir()

	root := cmd.NewRootCmd()
	root.SetArgs([]string{"extract", "--source", srcDir, "--out", outDir, "--dry-run"})
	buf := &bytes.Buffer{}
	root.SetOut(buf)

	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run extract failed: %v", err)
	}

	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Errorf("dry-run wrote %d files/dirs, expected none", len(entries))
	}
}

func TestQlikview_ExtractSucceeds_ExitCode0(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	srcDir, _ := filepath.Abs(qlikviewTestdata)
	outDir := t.TempDir()

	root := cmd.NewRootCmd()
	root.SetArgs([]string{"extract", "--source", srcDir, "--out", outDir})
	buf := &bytes.Buffer{}
	root.SetOut(buf)

	err := root.Execute()
	if err != nil {
		t.Errorf("expected exit 0 (nil error) for all-valid QVW files, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Extracted 2 apps") {
		t.Errorf("expected 'Extracted 2 apps' in summary, got: %q", out)
	}
}

func TestQlikview_ParseQVF_ArtifactCounts(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	path := filepath.Join(qlikviewTestdata, "Qlik_Sense_Content_Monitor.qvf")
	data, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("ParseQVF failed: %v", err)
	}

	if len(data.Measures) != 171 {
		t.Errorf("expected 171 measures, got %d", len(data.Measures))
	}
	if len(data.Dimensions) != 78 {
		t.Errorf("expected 78 dimensions, got %d", len(data.Dimensions))
	}
	if len(data.Variables) != 43 {
		t.Errorf("expected 43 variables, got %d", len(data.Variables))
	}
}

func TestQlikview_ParseQVF_Sheets(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	path := filepath.Join(qlikviewTestdata, "Qlik_Sense_Content_Monitor.qvf")
	data, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("ParseQVF failed: %v", err)
	}

	if len(data.Sheets) != 16 {
		t.Fatalf("expected 16 sheets, got %d", len(data.Sheets))
	}

	// Locate the Applications sheet and assert it has classified data objects.
	var apps *extractor.Sheet
	for i := range data.Sheets {
		if data.Sheets[i].Title == "Applications" {
			apps = &data.Sheets[i]
			break
		}
	}
	if apps == nil {
		t.Fatal("sheet 'Applications' not found")
	}

	classified := 0
	for _, o := range apps.Objects {
		if !o.Unclassified {
			classified++
		}
	}
	if classified == 0 {
		t.Errorf("Applications sheet has no classified objects: %+v", apps.Objects)
	}

	// At least one object across the app must resolve a master-visualisation
	// reference into concrete measures (proves qExtendsId resolution works).
	resolvedMasterRef := false
	for _, s := range data.Sheets {
		for _, o := range s.Objects {
			if o.MasterObjectID != "" && (len(o.Measures) > 0 || len(o.Dimensions) > 0) {
				resolvedMasterRef = true
			}
		}
	}
	if !resolvedMasterRef {
		t.Error("expected at least one resolved master-visualisation reference")
	}
}

func TestQlikview_ParseQVF_SpotCheckMeasure(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	path := filepath.Join(qlikviewTestdata, "Qlik_Sense_Content_Monitor.qvf")
	data, err := extractor.ParseQVF(path)
	if err != nil {
		t.Fatalf("ParseQVF failed: %v", err)
	}

	var sessions *extractor.Measure
	for i := range data.Measures {
		if data.Measures[i].Label == "Sessions" {
			sessions = &data.Measures[i]
			break
		}
	}
	if sessions == nil {
		t.Fatal("measure with label 'Sessions' not found")
	}
	if sessions.Def != "Count(DISTINCT SessionID)" {
		t.Errorf("Sessions.Def = %q, want %q", sessions.Def, "Count(DISTINCT SessionID)")
	}
}

func TestQlikview_QVW_NoJSONArtifacts(t *testing.T) {
	skipIfNoQlikviewFixtures(t)

	srcDir, _ := filepath.Abs(qlikviewTestdata)
	outDir := t.TempDir()

	root := cmd.NewRootCmd()
	root.SetArgs([]string{"extract", "--source", srcDir, "--out", outDir})
	buf := &bytes.Buffer{}
	root.SetOut(buf)

	if err := root.Execute(); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	// The QVW output folder should contain only script.qvs, no JSON files.
	qvwOutDir := filepath.Join(outDir, "Governance.Dashboard.2.1.4.qvw")
	entries, err := os.ReadDir(qvwOutDir)
	if err != nil {
		t.Fatalf("output dir not found: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("unexpected JSON file in QVW output: %s", e.Name())
		}
	}
	// Must have script.qvs
	scriptPath := filepath.Join(qvwOutDir, "script.qvs")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Errorf("script.qvs not found in QVW output dir: %v", err)
	}
}
