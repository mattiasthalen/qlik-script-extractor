package extractor

import (
	"encoding/json"
	"sort"
)

// Sheet is a single Qlik Sense sheet with the visualisation objects placed on it.
type Sheet struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Objects     []Visualization `json:"objects"`
}

// Visualization is a chart/object on a sheet (barchart, table, kpi, pivot, ...).
// When a referenced object's block is not recognised, Unclassified is true and
// only ID/Type (from the sheet cell hint) are populated — extraction never fails.
type Visualization struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Title          string     `json:"title,omitempty"`
	Dimensions     []VizField `json:"dimensions"`
	Measures       []VizField `json:"measures"`
	MasterObjectID string     `json:"masterObjectId,omitempty"`
	Unclassified   bool       `json:"unclassified,omitempty"`
}

// VizField is a dimension or measure used by a visualisation. It is either an
// inline definition (Def/Label) or a reference to a master item (LibraryID).
type VizField struct {
	Label     string `json:"label,omitempty"`
	Def       string `json:"def,omitempty"`
	LibraryID string `json:"libraryId,omitempty"`
}

// sheetRaw is the intermediate form captured during the byte-scan, before the
// referenced object blocks (which may appear anywhere in the file) are resolved.
type sheetRaw struct {
	id          string
	title       string
	description string
	rank        float64
	cells       []sheetCell
}

type sheetCell struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// parseSheet decodes a sheet block into its intermediate form. Returns false if
// the block does not look like a sheet.
func parseSheet(id string, raw map[string]json.RawMessage) (sheetRaw, bool) {
	s := sheetRaw{id: id}

	var meta struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if raw["qMetaDef"] != nil {
		_ = json.Unmarshal(raw["qMetaDef"], &meta)
	}
	s.title = meta.Title
	s.description = meta.Description

	if r, ok := raw["rank"]; ok {
		_ = json.Unmarshal(r, &s.rank)
	}

	if cellsRaw, ok := raw["cells"]; ok {
		_ = json.Unmarshal(cellsRaw, &s.cells)
	}
	return s, true
}

// parseVisualization decodes an object block into a Visualization. Returns false
// if the block does not look like a chart/object (no hypercube or list object).
func parseVisualization(id, qType string, raw map[string]json.RawMessage) (Visualization, bool) {
	hc := findRawKey(raw, "qHyperCubeDef")
	lo := findRawKey(raw, "qListObjectDef")

	// A master-visualisation placed on a sheet is a thin wrapper that carries
	// only qExtendsId; its data lives on the referenced masterobject, resolved
	// later by resolveExtends.
	var extends string
	if e, ok := raw["qExtendsId"]; ok {
		_ = json.Unmarshal(e, &extends)
	}

	if hc == nil && lo == nil && extends == "" {
		return Visualization{}, false
	}

	viz := Visualization{
		ID:             id,
		Type:           vizType(qType, raw),
		Title:          vizTitle(raw),
		Dimensions:     []VizField{},
		Measures:       []VizField{},
		MasterObjectID: extends,
	}

	if hc != nil {
		dims, meas := parseHyperCube(hc)
		viz.Dimensions = dims
		viz.Measures = meas
	}
	if lo != nil {
		// Filter panes / list boxes carry a single dimension in a list object.
		viz.Dimensions = append(viz.Dimensions, parseListObject(lo)...)
	}
	return viz, true
}

// vizType prefers the explicit "visualization" field, falling back to qInfo.qType.
func vizType(qType string, raw map[string]json.RawMessage) string {
	if v, ok := raw["visualization"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return s
		}
	}
	return qType
}

// vizTitle resolves an object title, which may be a plain string or a Qlik
// string-expression object ({"qStringExpression":{"qExpr":"=..."}}).
func vizTitle(raw map[string]json.RawMessage) string {
	if t, ok := raw["title"]; ok {
		if s := decodeTitle(t); s != "" {
			return s
		}
	}
	if m, ok := raw["qMetaDef"]; ok {
		var meta struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(m, &meta) == nil && meta.Title != "" {
			return meta.Title
		}
	}
	return ""
}

func decodeTitle(t json.RawMessage) string {
	var s string
	if json.Unmarshal(t, &s) == nil {
		return s
	}
	var expr struct {
		QStringExpression struct {
			QExpr string `json:"qExpr"`
		} `json:"qStringExpression"`
	}
	if json.Unmarshal(t, &expr) == nil {
		return expr.QStringExpression.QExpr
	}
	return ""
}

// parseHyperCube extracts the dimensions and measures from a qHyperCubeDef.
func parseHyperCube(hc json.RawMessage) (dims, meas []VizField) {
	dims = []VizField{}
	meas = []VizField{}

	var cube struct {
		QDimensions []struct {
			QLibraryID string `json:"qLibraryId"`
			QDef       struct {
				QFieldDefs   []string `json:"qFieldDefs"`
				QFieldLabels []string `json:"qFieldLabels"`
			} `json:"qDef"`
		} `json:"qDimensions"`
		QMeasures []struct {
			QLibraryID string `json:"qLibraryId"`
			QDef       struct {
				QLabel string `json:"qLabel"`
				QDef   string `json:"qDef"`
			} `json:"qDef"`
		} `json:"qMeasures"`
	}
	if json.Unmarshal(hc, &cube) != nil {
		return dims, meas
	}

	for _, d := range cube.QDimensions {
		f := VizField{LibraryID: d.QLibraryID}
		if len(d.QDef.QFieldDefs) > 0 {
			f.Def = joinFields(d.QDef.QFieldDefs)
		}
		if len(d.QDef.QFieldLabels) > 0 {
			f.Label = firstNonEmpty(d.QDef.QFieldLabels)
		}
		dims = append(dims, f)
	}
	for _, m := range cube.QMeasures {
		meas = append(meas, VizField{
			LibraryID: m.QLibraryID,
			Label:     m.QDef.QLabel,
			Def:       m.QDef.QDef,
		})
	}
	return dims, meas
}

// parseListObject extracts the single dimension of a qListObjectDef.
func parseListObject(lo json.RawMessage) []VizField {
	var obj struct {
		QLibraryID string `json:"qLibraryId"`
		QDef       struct {
			QFieldDefs   []string `json:"qFieldDefs"`
			QFieldLabels []string `json:"qFieldLabels"`
		} `json:"qDef"`
	}
	if json.Unmarshal(lo, &obj) != nil {
		return nil
	}
	if obj.QLibraryID == "" && len(obj.QDef.QFieldDefs) == 0 {
		return nil
	}
	return []VizField{{
		LibraryID: obj.QLibraryID,
		Def:       joinFields(obj.QDef.QFieldDefs),
		Label:     firstNonEmpty(obj.QDef.QFieldLabels),
	}}
}

// findRawKey walks the decoded JSON object tree and returns the raw message for
// the first occurrence of key, at any depth. Object shapes vary across Qlik
// versions, so a recursive search is more robust than a fixed path.
func findRawKey(raw map[string]json.RawMessage, key string) json.RawMessage {
	if v, ok := raw[key]; ok {
		return v
	}
	for _, v := range raw {
		if len(v) == 0 || v[0] != '{' {
			continue
		}
		var child map[string]json.RawMessage
		if json.Unmarshal(v, &child) != nil {
			continue
		}
		if found := findRawKey(child, key); found != nil {
			return found
		}
	}
	return nil
}

// resolveExtends fills in master-visualisation wrappers (objects that only
// carry qExtendsId) from the masterobject they reference, so a KPI or filter
// pane dropped onto a sheet from the library documents its real data.
func resolveExtends(viz map[string]Visualization) {
	for id, v := range viz {
		if v.MasterObjectID == "" {
			continue
		}
		master, ok := viz[v.MasterObjectID]
		if !ok {
			continue
		}
		if len(v.Dimensions) == 0 {
			v.Dimensions = master.Dimensions
		}
		if len(v.Measures) == 0 {
			v.Measures = master.Measures
		}
		if v.Title == "" {
			v.Title = master.Title
		}
		viz[id] = v
	}
}

// resolveSheets attaches captured visualisation objects to the sheets that
// reference them, producing a deterministic per-sheet inventory. Objects
// referenced by a sheet but never recognised are recorded as unclassified.
func resolveSheets(sheets map[string]*sheetRaw, viz map[string]Visualization) []Sheet {
	out := make([]Sheet, 0, len(sheets))
	for _, sr := range sheets {
		s := Sheet{
			ID:          sr.id,
			Title:       sr.title,
			Description: sr.description,
			Objects:     []Visualization{},
		}
		for _, cell := range sr.cells {
			if v, ok := viz[cell.Name]; ok {
				s.Objects = append(s.Objects, v)
				continue
			}
			t := cell.Type
			if t == "" {
				t = "unclassified"
			}
			s.Objects = append(s.Objects, Visualization{
				ID:           cell.Name,
				Type:         t,
				Dimensions:   []VizField{},
				Measures:     []VizField{},
				Unclassified: true,
			})
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := sheets[out[i].ID].rank, sheets[out[j].ID].rank
		if ri != rj {
			return ri < rj
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func joinFields(defs []string) string {
	switch len(defs) {
	case 0:
		return ""
	case 1:
		return defs[0]
	default:
		out := defs[0]
		for _, d := range defs[1:] {
			out += ", " + d
		}
		return out
	}
}

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
