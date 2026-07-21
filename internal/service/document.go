// Package service hosts the Cloud Run HTTP service around the engine-free QVF
// parser: request handling, authentication, object storage and the versioned
// output document.
package service

import (
	"time"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
	"github.com/mattiasthalen/qlik-parser/internal/redact"
)

// SchemaVersion is the version of the Document JSON schema. Bump the minor for
// additive changes, the major for breaking ones.
const SchemaVersion = "1.0.0"

// Document is the versioned, self-describing result of documenting one Qlik app.
type Document struct {
	SchemaVersion string                  `json:"schemaVersion"`
	Source        string                  `json:"source"`
	App           string                  `json:"app"`
	GeneratedAt   time.Time               `json:"generatedAt"`
	Redaction     string                  `json:"redaction"`
	Script        string                  `json:"script"`
	Measures      []extractor.Measure     `json:"measures"`
	Dimensions    []extractor.Dimension   `json:"dimensions"`
	Variables     []extractor.Variable    `json:"variables"`
	Sheets        []extractor.Sheet       `json:"sheets"`
	Lineage       extractor.ScriptLineage `json:"lineage"`
	Warnings      []string                `json:"warnings"`
	// Markdown is populated only when the AI documentation stage is enabled.
	Markdown string `json:"markdown,omitempty"`
}

// BuildDocument assembles a Document from parsed data, applying the requested
// secret-handling mode. In redact mode the embedded script is rewritten with
// secret values replaced; in flag mode the script is left intact. Warnings list
// the detected secrets either way.
func BuildDocument(source, app string, data *extractor.QVFData, mode redact.Mode) *Document {
	script, findings := redact.Apply(data.Script, mode)

	return &Document{
		SchemaVersion: SchemaVersion,
		Source:        source,
		App:           app,
		GeneratedAt:   time.Now().UTC(),
		Redaction:     string(mode),
		Script:        script,
		Measures:      data.Measures,
		Dimensions:    data.Dimensions,
		Variables:     data.Variables,
		Sheets:        data.Sheets,
		Lineage:       data.Lineage,
		Warnings:      redact.Warnings("script", findings),
	}
}
