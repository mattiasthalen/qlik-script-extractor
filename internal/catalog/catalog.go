// Package catalog builds a combined index of the master items (measures,
// dimensions) and variables defined across many Qlik apps, so that duplicated
// and conflicting definitions surface in one place.
package catalog

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
)

// Catalog is the combined cross-app index.
type Catalog struct {
	Apps       []string `json:"apps"`
	Measures   []Entry  `json:"measures"`
	Dimensions []Entry  `json:"dimensions"`
	Variables  []Entry  `json:"variables"`
	// Conflicts lists the entries (across all kinds) whose name maps to more
	// than one distinct definition — the ones worth reconciling.
	Conflicts []Entry `json:"conflicts"`
}

// Entry is one named artifact and every definition of it seen across apps.
type Entry struct {
	Name        string       `json:"name"`
	Kind        string       `json:"kind"` // measure, dimension, variable
	Definitions []Definition `json:"definitions"`
	Conflicting bool         `json:"conflicting"` // true when Definitions has >1 entry
}

// Definition is a single definition text and the apps that use it.
type Definition struct {
	Definition string   `json:"definition"`
	Apps       []string `json:"apps"`
}

// FlatRow is a denormalised catalog row, one per (name, definition, app),
// suitable for streaming into BigQuery (newline-delimited JSON).
type FlatRow struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Definition  string `json:"definition"`
	App         string `json:"app"`
	Conflicting bool   `json:"conflicting"`
}

// Builder accumulates artifacts from many apps before producing a Catalog.
type Builder struct {
	apps map[string]bool
	// key: kind -> name -> definition -> set of apps
	items map[string]map[string]map[string]map[string]bool
}

// NewBuilder returns an empty catalog Builder.
func NewBuilder() *Builder {
	return &Builder{
		apps:  map[string]bool{},
		items: map[string]map[string]map[string]map[string]bool{},
	}
}

// AddApp folds one app's extracted data into the catalog under the given app
// name (typically the source filename).
func (b *Builder) AddApp(app string, data *extractor.QVFData) {
	if data == nil {
		return
	}
	b.apps[app] = true
	for _, m := range data.Measures {
		b.add("measure", displayName(m.Label, m.ID), m.Def, app)
	}
	for _, d := range data.Dimensions {
		b.add("dimension", displayName(d.Label, d.ID), strings.Join(d.Fields, ", "), app)
	}
	for _, v := range data.Variables {
		b.add("variable", v.Name, strings.TrimSpace(string(v.Value)), app)
	}
}

func (b *Builder) add(kind, name, def, app string) {
	if name == "" {
		return
	}
	if b.items[kind] == nil {
		b.items[kind] = map[string]map[string]map[string]bool{}
	}
	if b.items[kind][name] == nil {
		b.items[kind][name] = map[string]map[string]bool{}
	}
	if b.items[kind][name][def] == nil {
		b.items[kind][name][def] = map[string]bool{}
	}
	b.items[kind][name][def][app] = true
}

// Build produces the deterministic, sorted Catalog.
func (b *Builder) Build() Catalog {
	cat := Catalog{
		Apps:       sortedKeys(b.apps),
		Measures:   b.entries("measure"),
		Dimensions: b.entries("dimension"),
		Variables:  b.entries("variable"),
		Conflicts:  []Entry{},
	}
	for _, group := range [][]Entry{cat.Measures, cat.Dimensions, cat.Variables} {
		for _, e := range group {
			if e.Conflicting {
				cat.Conflicts = append(cat.Conflicts, e)
			}
		}
	}
	return cat
}

func (b *Builder) entries(kind string) []Entry {
	names := b.items[kind]
	out := make([]Entry, 0, len(names))
	for name, defs := range names {
		e := Entry{Name: name, Kind: kind, Definitions: make([]Definition, 0, len(defs))}
		for def, apps := range defs {
			e.Definitions = append(e.Definitions, Definition{
				Definition: def,
				Apps:       sortedKeys(apps),
			})
		}
		sort.Slice(e.Definitions, func(i, j int) bool {
			return e.Definitions[i].Definition < e.Definitions[j].Definition
		})
		e.Conflicting = len(e.Definitions) > 1
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FlatRows returns the catalog denormalised for BigQuery loading.
func (c Catalog) FlatRows() []FlatRow {
	var rows []FlatRow
	for _, group := range [][]Entry{c.Measures, c.Dimensions, c.Variables} {
		for _, e := range group {
			for _, d := range e.Definitions {
				for _, app := range d.Apps {
					rows = append(rows, FlatRow{
						Kind:        e.Kind,
						Name:        e.Name,
						Definition:  d.Definition,
						App:         app,
						Conflicting: e.Conflicting,
					})
				}
			}
		}
	}
	return rows
}

// NDJSON renders the flat rows as newline-delimited JSON for `bq load`.
func (c Catalog) NDJSON() ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, row := range c.FlatRows() {
		if err := enc.Encode(row); err != nil {
			return nil, err
		}
	}
	return []byte(b.String()), nil
}

func displayName(label, id string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	return id
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
