package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
	"github.com/mattiasthalen/qlik-parser/internal/redact"
)

func TestParseGSURI(t *testing.T) {
	b, o, err := parseGSURI("gs://my-bucket/path/to/app.qvf")
	if err != nil || b != "my-bucket" || o != "path/to/app.qvf" {
		t.Fatalf("parseGSURI = %q,%q,%v", b, o, err)
	}
	if _, _, err := parseGSURI("https://x/y"); err == nil {
		t.Error("expected error for non-gs URI")
	}
	if _, _, err := parseGSURI("gs://only-bucket"); err == nil {
		t.Error("expected error for missing object")
	}
}

func TestAppName(t *testing.T) {
	cases := map[string]string{
		"gs://b/dir/Sales.qvf":               "Sales",
		"https://host/Report.qvf?sig=abc123": "Report",
		"/local/path/My App.qvf":             "My App",
		"":                                   "app",
	}
	for in, want := range cases {
		if got := appName(in); got != want {
			t.Errorf("appName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinURI(t *testing.T) {
	if got := joinURI("gs://b/out", "a.json"); got != "gs://b/out/a.json" {
		t.Errorf("joinURI = %q", got)
	}
	if got := joinURI("gs://b/out/", "a.json"); got != "gs://b/out/a.json" {
		t.Errorf("joinURI trailing slash = %q", got)
	}
}

func TestDecodeGCSEvent(t *testing.T) {
	obj := gcsObject{Bucket: "b", Name: "app.qvf"}
	raw, _ := json.Marshal(obj)

	// Direct / binary CloudEvent (body is the object).
	got, err := decodeGCSEvent("application/json", raw)
	if err != nil || got != obj {
		t.Errorf("direct: %+v %v", got, err)
	}

	// Structured CloudEvents JSON.
	env, _ := json.Marshal(map[string]any{"data": obj})
	got, err = decodeGCSEvent("application/cloudevents+json", env)
	if err != nil || got != obj {
		t.Errorf("structured: %+v %v", got, err)
	}

	// Pub/Sub push envelope.
	ps, _ := json.Marshal(map[string]any{
		"message": map[string]any{"data": base64.StdEncoding.EncodeToString(raw)},
	})
	got, err = decodeGCSEvent("application/json", ps)
	if err != nil || got != obj {
		t.Errorf("pubsub: %+v %v", got, err)
	}

	// Missing fields.
	if _, err := decodeGCSEvent("application/json", []byte(`{"bucket":"b"}`)); err == nil {
		t.Error("expected error for missing name")
	}
}

func TestBuildDocument_FlagVsRedact(t *testing.T) {
	data := &extractor.QVFData{
		Script:     "Password=hunter2xyz;\nLOAD 1;",
		Measures:   []extractor.Measure{},
		Dimensions: []extractor.Dimension{},
		Variables:  []extractor.Variable{},
		Sheets:     []extractor.Sheet{},
	}

	flag := BuildDocument("gs://b/App.qvf", "App", data, redact.ModeFlag)
	if flag.Script != data.Script {
		t.Errorf("flag mode altered script: %q", flag.Script)
	}
	if len(flag.Warnings) == 0 {
		t.Error("expected a warning in flag mode")
	}
	if flag.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %q", flag.SchemaVersion)
	}

	red := BuildDocument("gs://b/App.qvf", "App", data, redact.ModeRedact)
	if red.Script == data.Script {
		t.Error("redact mode did not change script")
	}
	if red.Redaction != "redact" {
		t.Errorf("redaction = %q", red.Redaction)
	}
}

func TestAnthropicDocumenter_Markdown(t *testing.T) {
	var gotBody map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "# App\nGenerated docs."}},
		})
	}))
	defer fake.Close()

	d := NewAnthropicDocumenter(AIConfig{APIKey: "test-key", BaseURL: fake.URL, Model: "claude-sonnet-5"}, nil)
	doc := &Document{App: "App", Script: "LOAD 1;", Measures: []extractor.Measure{}}

	md, err := d.Markdown(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if md != "# App\nGenerated docs." {
		t.Errorf("markdown = %q", md)
	}
	if gotBody["model"] != "claude-sonnet-5" {
		t.Errorf("model not sent: %v", gotBody["model"])
	}
}

func TestAnthropicDocumenter_ErrorStatus(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer fake.Close()

	d := NewAnthropicDocumenter(AIConfig{APIKey: "k", BaseURL: fake.URL}, nil)
	_, err := d.Markdown(context.Background(), &Document{App: "X"})
	if err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestUploadLocal(t *testing.T) {
	s := NewStorage()
	dir := t.TempDir()
	uri := dir + "/sub/out.json"
	if err := s.Upload(context.Background(), uri, []byte(`{"ok":true}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(mustOpen(t, uri))
	if err != nil || string(b) != `{"ok":true}` {
		t.Errorf("upload local failed: %q %v", b, err)
	}
}

func mustOpen(t *testing.T, path string) io.Reader {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
