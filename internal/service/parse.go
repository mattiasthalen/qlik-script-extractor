package service

import (
	"context"
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/exp/mmap"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
	"github.com/mattiasthalen/qlik-parser/internal/redact"
)

// Documenter turns a structured Document into human-readable Markdown. It is the
// seam for the optional AI documentation stage; when nil the stage is off.
type Documenter interface {
	Markdown(ctx context.Context, doc *Document) (string, error)
}

// ParseRequest is the JSON body of POST /parse.
type ParseRequest struct {
	// Source is a gs:// path, an http(s) signed URL, or a local path.
	Source string `json:"source"`
	// Output, when set, is the gs:// prefix or local directory to write
	// <app>.json (and <app>.md) into. Empty means "return inline only".
	Output string `json:"output,omitempty"`
	// Redaction is "flag" (default) or "redact".
	Redaction string `json:"redaction,omitempty"`
	// Markdown requests the AI documentation stage (ignored when it is off).
	Markdown bool `json:"markdown,omitempty"`
	// Inline, when true, includes the full Document in the HTTP response.
	Inline bool `json:"inline,omitempty"`
}

// ParseResponse is the JSON body returned by POST /parse.
type ParseResponse struct {
	App      string    `json:"app"`
	Source   string    `json:"source"`
	Outputs  []string  `json:"outputs,omitempty"`
	Warnings []string  `json:"warnings"`
	Document *Document `json:"document,omitempty"`
}

// process runs the full documentation pipeline for one app: fetch, mmap-parse,
// redact, optionally document with AI, and write outputs.
func (s *Server) process(ctx context.Context, req ParseRequest, defaultOutput string) (*ParseResponse, error) {
	if strings.TrimSpace(req.Source) == "" {
		return nil, fmt.Errorf("source is required")
	}
	mode := redact.ModeFlag
	if strings.EqualFold(req.Redaction, string(redact.ModeRedact)) {
		mode = redact.ModeRedact
	}

	localPath, isTemp, err := s.store.Materialize(ctx, req.Source, s.cfg.TmpDir)
	if err != nil {
		return nil, err
	}
	if isTemp {
		defer func() { _ = os.Remove(localPath) }()
	}

	data, err := parseLocalFile(localPath)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", req.Source, err)
	}

	app := appName(req.Source)
	doc := BuildDocument(req.Source, app, data, mode)

	if req.Markdown && s.documenter != nil {
		md, aerr := s.documenter.Markdown(ctx, doc)
		if aerr != nil {
			s.log.WarnContext(ctx, "ai documentation stage failed", "app", app, "error", aerr)
			doc.Warnings = append(doc.Warnings, "ai documentation stage failed: "+aerr.Error())
		} else {
			doc.Markdown = md
		}
	}

	resp := &ParseResponse{App: app, Source: req.Source, Warnings: doc.Warnings}
	if req.Inline {
		resp.Document = doc
	}

	output := req.Output
	if output == "" {
		output = defaultOutput
	}
	if output != "" {
		outs, werr := s.writeOutputs(ctx, output, app, doc)
		if werr != nil {
			return nil, werr
		}
		resp.Outputs = outs
	}
	return resp, nil
}

// writeOutputs writes the document JSON (and Markdown, if present) under the
// given output prefix, returning the URIs written.
func (s *Server) writeOutputs(ctx context.Context, output, app string, doc *Document) ([]string, error) {
	jsonBytes, err := marshalIndent(doc)
	if err != nil {
		return nil, err
	}
	var outs []string

	jsonURI := joinURI(output, app+".json")
	if err := s.store.Upload(ctx, jsonURI, jsonBytes, "application/json"); err != nil {
		return nil, err
	}
	outs = append(outs, jsonURI)

	if doc.Markdown != "" {
		mdURI := joinURI(output, app+".md")
		if err := s.store.Upload(ctx, mdURI, []byte(doc.Markdown), "text/markdown"); err != nil {
			return nil, err
		}
		outs = append(outs, mdURI)
	}
	return outs, nil
}

// parseLocalFile memory-maps a local .qvf and parses it via the ReaderAt path,
// so multi-gigabyte files are paged by the OS rather than read into the heap.
func parseLocalFile(path string) (*extractor.QVFData, error) {
	m, err := mmap.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.Close() }()
	return extractor.ParseReaderAt(m, int64(m.Len()))
}

// appName derives the app name (base filename without extension) from a URI.
func appName(uri string) string {
	base := filepath.Base(strings.SplitN(uri, "?", 2)[0])
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" || base == "." || base == "/" {
		return "app"
	}
	return base
}

// joinURI appends name to a prefix that may be a gs:// URI or a local path.
func joinURI(prefix, name string) string {
	return strings.TrimRight(prefix, "/") + "/" + name
}

// constantTimeEqual compares two secrets without leaking length-independent
// timing information.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
