// Package redact scans Qlik load scripts and connection strings for embedded
// credentials, tokens and passwords, and either flags or removes them before
// the extracted content leaves the service.
package redact

import (
	"fmt"
	"regexp"
	"strings"
)

// Mode controls what happens when a secret is found.
type Mode string

const (
	// ModeFlag leaves the text untouched and only reports findings.
	ModeFlag Mode = "flag"
	// ModeRedact replaces the secret value with the redaction placeholder.
	ModeRedact Mode = "redact"
)

// Placeholder replaces a secret value in ModeRedact.
const Placeholder = "[REDACTED]"

// Finding describes one detected secret.
type Finding struct {
	Kind    string `json:"kind"`    // password, token, apiKey, awsAccessKey, privateKey, urlCredentials
	Line    int    `json:"line"`    // 1-based line number in the scanned text
	Preview string `json:"preview"` // masked snippet, safe to log/emit
}

// secretPattern matches a secret. Group 1 is the sensitive value to mask; if the
// pattern has no capture group the whole match is treated as sensitive.
type secretPattern struct {
	kind string
	re   *regexp.Regexp
}

// patterns are ordered most-specific first. Each is case-insensitive where it
// makes sense. Group 1, when present, is the value to redact.
var patterns = []secretPattern{
	{"privateKey", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"awsAccessKey", regexp.MustCompile(`\b(AKIA[0-9A-Z]{16})\b`)},
	{"urlCredentials", regexp.MustCompile(`://[^\s:/@]+:([^\s:/@]+)@`)},
	{"password", regexp.MustCompile(`(?i)\b(?:password|pwd|passwd)\b\s*[=:]\s*'([^']+)'`)},
	{"password", regexp.MustCompile(`(?i)\b(?:password|pwd|passwd)\b\s*[=:]\s*"([^"]+)"`)},
	{"password", regexp.MustCompile(`(?i)\b(?:password|pwd|passwd)\b\s*[=:]\s*([^\s;'"\]]+)`)},
	{"apiKey", regexp.MustCompile(`(?i)\b(?:api[_-]?key|apikey|client[_-]?secret|secret[_-]?key)\b\s*[=:]\s*'([^']+)'`)},
	{"apiKey", regexp.MustCompile(`(?i)\b(?:api[_-]?key|apikey|client[_-]?secret|secret[_-]?key)\b\s*[=:]\s*"([^"]+)"`)},
	{"apiKey", regexp.MustCompile(`(?i)\b(?:api[_-]?key|apikey|client[_-]?secret|secret[_-]?key)\b\s*[=:]\s*([^\s;'"\]]+)`)},
	{"token", regexp.MustCompile(`(?i)\b(?:access[_-]?token|auth[_-]?token|token)\b\s*[=:]\s*'([^']+)'`)},
	{"token", regexp.MustCompile(`(?i)\b(?:access[_-]?token|auth[_-]?token|token)\b\s*[=:]\s*"([^"]+)"`)},
	{"token", regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{12,})`)},
}

// Scan returns every secret found in text, without modifying it.
func Scan(text string) []Finding {
	_, findings := apply(text, ModeFlag)
	return findings
}

// Apply scans text and, in ModeRedact, returns a copy with secret values
// replaced by Placeholder. In ModeFlag the returned text equals the input.
// The findings are returned in both modes.
func Apply(text string, mode Mode) (string, []Finding) {
	return apply(text, mode)
}

func apply(text string, mode Mode) (string, []Finding) {
	var spans []rangeSpan

	for _, p := range patterns {
		for _, idx := range p.re.FindAllStringSubmatchIndex(text, -1) {
			// Prefer the value capture group; fall back to the whole match.
			start, end := idx[0], idx[1]
			if len(idx) >= 4 && idx[2] >= 0 {
				start, end = idx[2], idx[3]
			}
			spans = append(spans, rangeSpan{start, end, p.kind})
		}
	}
	if len(spans) == 0 {
		return text, nil
	}

	// Drop spans already covered by an earlier (more specific) one so a value
	// is not reported twice by overlapping patterns.
	kept := dedupeSpans(spans)

	findings := make([]Finding, 0, len(kept))
	for _, s := range kept {
		findings = append(findings, Finding{
			Kind:    s.kind,
			Line:    1 + strings.Count(text[:s.start], "\n"),
			Preview: mask(text[s.start:s.end]),
		})
	}

	if mode != ModeRedact {
		return text, findings
	}

	// Rebuild the text with each kept span replaced, right-to-left so earlier
	// offsets stay valid.
	var b strings.Builder
	prev := 0
	for _, s := range kept {
		b.WriteString(text[prev:s.start])
		b.WriteString(Placeholder)
		prev = s.end
	}
	b.WriteString(text[prev:])
	return b.String(), findings
}

type rangeSpan struct {
	start, end int
	kind       string
}

// dedupeSpans sorts spans by start and removes any that overlap a previously
// kept span.
func dedupeSpans(spans []rangeSpan) []rangeSpan {
	// insertion sort by start (span counts are tiny)
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start < spans[j-1].start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	var kept []rangeSpan
	lastEnd := -1
	for _, s := range spans {
		if s.start < lastEnd {
			continue // overlaps an already-kept span
		}
		kept = append(kept, s)
		lastEnd = s.end
	}
	return kept
}

// mask returns a preview that reveals at most the first two characters.
func mask(secret string) string {
	secret = strings.TrimSpace(secret)
	switch {
	case len(secret) == 0:
		return "***"
	case len(secret) <= 4:
		return "****"
	default:
		return fmt.Sprintf("%s****", secret[:2])
	}
}

// Warnings renders findings into human-readable warning strings suitable for an
// output "warnings" list.
func Warnings(source string, findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, fmt.Sprintf("possible %s in %s (line %d): %s", f.Kind, source, f.Line, f.Preview))
	}
	return out
}
