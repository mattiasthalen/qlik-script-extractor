package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AIConfig configures the optional Anthropic-backed documentation stage.
type AIConfig struct {
	APIKey    string
	Model     string
	BaseURL   string // default https://api.anthropic.com
	MaxTokens int
	Timeout   time.Duration
	HTTP      *http.Client
}

const (
	defaultAIModel      = "claude-sonnet-5"
	defaultAIMaxTokens  = 4096
	defaultAIBaseURL    = "https://api.anthropic.com"
	anthropicAPIVersion = "2023-06-01"
	maxScriptInPrompt   = 12000 // chars of script sent to the model
)

// anthropicDocumenter implements Documenter by calling the Anthropic Messages
// API over the standard library HTTP client (no SDK dependency).
type anthropicDocumenter struct {
	cfg AIConfig
	log *slog.Logger
}

// DocumenterFromEnv returns an AI Documenter when QVF_AI_ENABLED is truthy, or
// (nil, nil) when the stage is off so extraction still works with the AI stage
// disabled. It errors only on misconfiguration (enabled but no API key).
func DocumenterFromEnv(log *slog.Logger) (Documenter, error) {
	if !truthy(os.Getenv("QVF_AI_ENABLED")) {
		return nil, nil
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("QVF_AI_ENABLED is set but ANTHROPIC_API_KEY is empty")
	}
	cfg := AIConfig{
		APIKey:    key,
		Model:     envOr("QVF_AI_MODEL", defaultAIModel),
		BaseURL:   envOr("QVF_AI_BASE_URL", defaultAIBaseURL),
		MaxTokens: envInt("QVF_AI_MAX_TOKENS", defaultAIMaxTokens),
		Timeout:   2 * time.Minute,
	}
	return NewAnthropicDocumenter(cfg, log), nil
}

// NewAnthropicDocumenter builds a Documenter with defaults filled in.
func NewAnthropicDocumenter(cfg AIConfig, log *slog.Logger) Documenter {
	if cfg.Model == "" {
		cfg.Model = defaultAIModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultAIBaseURL
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = defaultAIMaxTokens
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: cfg.Timeout}
	}
	if log == nil {
		log = slog.Default()
	}
	return &anthropicDocumenter{cfg: cfg, log: log}
}

const documenterSystemPrompt = `You are a technical writer documenting Qlik Sense applications for a data catalog.
You are given a structured JSON summary extracted from a .qvf file (load script,
data lineage, master measures and dimensions, variables, and a per-sheet
inventory of visualisations). Write clear, accurate Markdown documentation with
these sections:

# <App name>
## Overview
## Data sources
## Sheets
(one subsection per sheet, listing its visualisations and what each shows)
## Metric glossary
(master measures and dimensions with their definitions)

Base every statement on the provided data. Do not invent metrics, fields or
sources that are not present. Keep it concise and skimmable.`

func (d *anthropicDocumenter) Markdown(ctx context.Context, doc *Document) (string, error) {
	userContent, err := buildAIPrompt(doc)
	if err != nil {
		return "", err
	}

	reqBody := map[string]any{
		"model":      d.cfg.Model,
		"max_tokens": d.cfg.MaxTokens,
		"system":     documenterSystemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": userContent},
		},
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.cfg.BaseURL, "/")+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", d.cfg.APIKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := d.cfg.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling anthropic api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic api returned %s: %s", resp.Status, truncate(string(body), 400))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding anthropic response: %w", err)
	}
	var out strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("anthropic response contained no text")
	}
	return out.String(), nil
}

// buildAIPrompt renders a compact, token-bounded view of the document for the
// model: the full structured metadata but a truncated script.
func buildAIPrompt(doc *Document) (string, error) {
	summary := struct {
		App        string   `json:"app"`
		Script     string   `json:"script"`
		Lineage    any      `json:"lineage"`
		Measures   any      `json:"measures"`
		Dimensions any      `json:"dimensions"`
		Variables  any      `json:"variables"`
		Sheets     any      `json:"sheets"`
		Warnings   []string `json:"warnings,omitempty"`
	}{
		App:        doc.App,
		Script:     truncate(doc.Script, maxScriptInPrompt),
		Lineage:    doc.Lineage,
		Measures:   doc.Measures,
		Dimensions: doc.Dimensions,
		Variables:  doc.Variables,
		Sheets:     doc.Sheets,
		Warnings:   doc.Warnings,
	}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", err
	}
	return "Here is the extracted app data as JSON:\n\n```json\n" + string(b) + "\n```\n\nWrite the Markdown documentation.", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
