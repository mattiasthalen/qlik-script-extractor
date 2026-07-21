package service_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattiasthalen/qlik-parser/internal/service"
)

// writeSyntheticQVF builds a small multi-block .qvf on disk and returns its path.
func writeSyntheticQVF(t *testing.T, dir string) string {
	t.Helper()
	payloads := []map[string]any{
		{"qScript": "LIB CONNECT TO 'DC';\nT:\nLOAD a FROM [lib://x/a.qvd] (qvd);\n// Password=SuperSecret9"},
		{"qInfo": map[string]any{"qId": "m1", "qType": "measure"}, "qMeasure": map[string]any{"qLabel": "Sales", "qDef": "Sum(A)"}},
		{"qInfo": map[string]any{"qId": "s1", "qType": "sheet"}, "qMetaDef": map[string]any{"title": "S1"}, "cells": []map[string]any{{"name": "c1", "type": "kpi"}}},
		{"qInfo": map[string]any{"qId": "c1", "qType": "kpi"}, "qHyperCubeDef": map[string]any{"qMeasures": []map[string]any{{"qDef": map[string]any{"qDef": "Count(X)"}}}}},
	}
	var out bytes.Buffer
	for _, p := range payloads {
		out.Write(bytes.Repeat([]byte{0xFF}, 8))
		b, _ := json.Marshal(p)
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		_, _ = w.Write(b)
		_ = w.Close()
		out.Write(z.Bytes())
	}
	path := filepath.Join(dir, "myapp.qvf")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestServer(t *testing.T, cfg service.Config, doc service.Documenter) *httptest.Server {
	t.Helper()
	srv := service.NewServer(cfg, service.NewStorage(), doc)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestHealth(t *testing.T) {
	ts := newTestServer(t, service.Config{}, nil)
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" || body["schemaVersion"] == "" {
		t.Errorf("unexpected health body: %v", body)
	}
}

func TestParse_LocalFileRedactAndWrite(t *testing.T) {
	dir := t.TempDir()
	qvf := writeSyntheticQVF(t, dir)
	outDir := t.TempDir()

	ts := newTestServer(t, service.Config{}, nil)

	reqBody, _ := json.Marshal(service.ParseRequest{
		Source:    qvf,
		Output:    outDir,
		Redaction: "redact",
		Inline:    true,
	})
	resp, err := http.Post(ts.URL+"/parse", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("parse status = %d: %s", resp.StatusCode, b)
	}
	var pr service.ParseResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatal(err)
	}
	if pr.App != "myapp" {
		t.Errorf("app = %q, want myapp", pr.App)
	}
	if pr.Document == nil {
		t.Fatal("expected inline document")
	}
	if len(pr.Document.Sheets) != 1 || len(pr.Document.Measures) != 1 {
		t.Errorf("unexpected extraction: sheets=%d measures=%d", len(pr.Document.Sheets), len(pr.Document.Measures))
	}
	if pr.Document.Redaction != "redact" {
		t.Errorf("redaction = %q", pr.Document.Redaction)
	}
	// The output JSON must exist.
	if _, err := os.Stat(filepath.Join(outDir, "myapp.json")); err != nil {
		t.Errorf("output json not written: %v", err)
	}
	if len(pr.Outputs) == 0 {
		t.Error("expected outputs list")
	}
}

func TestParse_RequiresAPIKey(t *testing.T) {
	ts := newTestServer(t, service.Config{APIKey: "s3cr3t"}, nil)

	reqBody, _ := json.Marshal(service.ParseRequest{Source: "/tmp/whatever.qvf"})

	// No header -> 401.
	resp, _ := http.Post(ts.URL+"/parse", "application/json", bytes.NewReader(reqBody))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without key, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Wrong key -> 401.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/parse", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "wrong")
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", resp2.StatusCode)
	}
	_ = resp2.Body.Close()
}

func TestParse_MarkdownViaDocumenter(t *testing.T) {
	dir := t.TempDir()
	qvf := writeSyntheticQVF(t, dir)
	outDir := t.TempDir()

	ts := newTestServer(t, service.Config{}, stubDocumenter{md: "# Docs\nHello"})

	reqBody, _ := json.Marshal(service.ParseRequest{Source: qvf, Output: outDir, Markdown: true, Inline: true})
	resp, err := http.Post(ts.URL+"/parse", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var pr service.ParseResponse
	_ = json.NewDecoder(resp.Body).Decode(&pr)
	if pr.Document == nil || pr.Document.Markdown != "# Docs\nHello" {
		t.Fatalf("markdown not set: %+v", pr.Document)
	}
	if _, err := os.Stat(filepath.Join(outDir, "myapp.md")); err != nil {
		t.Errorf("markdown output not written: %v", err)
	}
}

func TestParse_BadJSON(t *testing.T) {
	ts := newTestServer(t, service.Config{}, nil)
	resp, _ := http.Post(ts.URL+"/parse", "application/json", strings.NewReader("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

type stubDocumenter struct{ md string }

func (s stubDocumenter) Markdown(_ context.Context, _ *service.Document) (string, error) {
	return s.md, nil
}
