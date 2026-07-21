package extractor

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// QVFData holds all artifacts extracted from a single .qvf file.
type QVFData struct {
	Script     string
	Measures   []Measure
	Dimensions []Dimension
	Variables  []Variable
	Sheets     []Sheet
	Lineage    ScriptLineage
}

// Measure represents a Qlik master measure.
type Measure struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Def         string   `json:"def"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
}

// Dimension represents a Qlik master dimension.
type Dimension struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Fields      []string `json:"fields"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
}

// Variable represents a Qlik variable.
type Variable struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Comment string          `json:"comment"`
	Value   json.RawMessage `json:"value"`
}

// ParseQVF reads a .qvf file and extracts all known artifact types in a single pass.
// It never returns NoScriptError; the Script field is simply empty if not found.
func ParseQVF(path string) (*QVFData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	result := &QVFData{
		Measures:   []Measure{},
		Dimensions: []Dimension{},
		Variables:  []Variable{},
		Sheets:     []Sheet{},
	}

	// Sheets reference object blocks by ID; both may appear anywhere in the
	// file and in any order, so collect them and resolve after the scan.
	sheets := map[string]*sheetRaw{}
	viz := map[string]Visualization{}

	validFLG := map[byte]bool{0x01: true, 0x5E: true, 0x9C: true, 0xDA: true}

	for i := 0; i < len(data)-1; i++ {
		if data[i] != 0x78 || !validFLG[data[i+1]] {
			continue
		}
		r, err := zlib.NewReader(bytes.NewReader(data[i:]))
		if err != nil {
			continue
		}
		decompressed, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			continue
		}
		trimmed := bytes.TrimRight(decompressed, "\x00")

		// Use a generic map to inspect top-level keys.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			continue
		}

		// Master items, the script and variables are stored as bare top-level
		// blocks. Sheets and their chart objects are instead wrapped in a
		// qRoot/qProperty/qChildren envelope, so classify the block itself and,
		// if present, every property tree nested under qRoot.
		classifyBlock(raw, result, sheets, viz)
		if rootRaw, ok := raw["qRoot"]; ok {
			for _, prop := range collectObjectProps(rootRaw) {
				classifyBlock(prop, result, sheets, viz)
			}
		}
	}

	resolveExtends(viz)
	result.Sheets = resolveSheets(sheets, viz)
	result.Lineage = ParseScriptLineage(result.Script)
	return result, nil
}

// classifyBlock routes one decoded property object into the result by its
// top-level keys. It is safe to call on both bare blocks and the property trees
// unwrapped from a qRoot envelope.
func classifyBlock(raw map[string]json.RawMessage, result *QVFData, sheets map[string]*sheetRaw, viz map[string]Visualization) {
	// Script block
	if scriptRaw, ok := raw["qScript"]; ok && result.Script == "" {
		var s string
		if err := json.Unmarshal(scriptRaw, &s); err == nil && s != "" {
			result.Script = s
			return
		}
	}

	// Variable list block
	if idRaw, ok := raw["qId"]; ok {
		var id string
		if err := json.Unmarshal(idRaw, &id); err == nil && id == "user_variablelist" {
			result.Variables = parseVariables(raw)
			return
		}
	}

	// Anything identified by qInfo.qType: master item, sheet or chart object.
	infoRaw, ok := raw["qInfo"]
	if !ok {
		return
	}
	var info struct {
		QID   string `json:"qId"`
		QType string `json:"qType"`
	}
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		return
	}
	switch info.QType {
	case "measure":
		if m, ok := parseMeasure(info.QID, raw); ok {
			result.Measures = append(result.Measures, m)
		}
	case "dimension":
		if d, ok := parseDimension(info.QID, raw); ok {
			result.Dimensions = append(result.Dimensions, d)
		}
	case "sheet":
		if s, ok := parseSheet(info.QID, raw); ok {
			sheets[info.QID] = &s
		}
	default:
		// Any other block that carries a hypercube/list object is a
		// visualisation; unrecognised shapes are simply skipped here and
		// surfaced as unclassified when a sheet references them.
		if v, ok := parseVisualization(info.QID, info.QType, raw); ok {
			viz[info.QID] = v
		}
	}
}

// collectObjectProps walks a qRoot envelope and returns every object property
// tree it contains: qRoot.qProperty plus, recursively, each qChildren entry's
// property tree. Charts placed on a sheet may be stored either as their own
// top-level blocks or nested as children, so both are gathered.
func collectObjectProps(root json.RawMessage) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	var walk func(node json.RawMessage)
	walk = func(node json.RawMessage) {
		var n map[string]json.RawMessage
		if json.Unmarshal(node, &n) != nil {
			return
		}
		if p, ok := n["qProperty"]; ok {
			var pm map[string]json.RawMessage
			if json.Unmarshal(p, &pm) == nil {
				out = append(out, pm)
			}
		}
		if ch, ok := n["qChildren"]; ok {
			var arr []json.RawMessage
			if json.Unmarshal(ch, &arr) == nil {
				for _, c := range arr {
					walk(c)
				}
			}
		}
	}
	walk(root)
	return out
}

// ExtractScriptFromQVF returns the embedded load script from a .qvf file.
// It delegates to ParseQVF and returns NoScriptError if no script is found.
func ExtractScriptFromQVF(path string) (string, error) {
	d, err := ParseQVF(path)
	if err != nil {
		return "", err
	}
	if d.Script == "" {
		return "", &NoScriptError{Path: path}
	}
	return d.Script, nil
}

func parseMeasure(id string, raw map[string]json.RawMessage) (Measure, bool) {
	var qMeasure struct {
		QLabel string   `json:"qLabel"`
		QDef   string   `json:"qDef"`
		QTags  []string `json:"qTags"`
	}
	if raw["qMeasure"] == nil {
		return Measure{}, false
	}
	if err := json.Unmarshal(raw["qMeasure"], &qMeasure); err != nil {
		return Measure{}, false
	}
	var meta struct {
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if raw["qMetaDef"] != nil {
		_ = json.Unmarshal(raw["qMetaDef"], &meta)
	}
	tags := qMeasure.QTags
	if tags == nil {
		tags = []string{}
	}
	return Measure{
		ID:          id,
		Label:       qMeasure.QLabel,
		Def:         qMeasure.QDef,
		Tags:        tags,
		Description: meta.Description,
	}, true
}

func parseDimension(id string, raw map[string]json.RawMessage) (Dimension, bool) {
	var qDim struct {
		QFieldDefs []string `json:"qFieldDefs"`
	}
	if raw["qDim"] == nil {
		return Dimension{}, false
	}
	if err := json.Unmarshal(raw["qDim"], &qDim); err != nil {
		return Dimension{}, false
	}
	var meta struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if raw["qMetaDef"] != nil {
		_ = json.Unmarshal(raw["qMetaDef"], &meta)
	}
	fields := qDim.QFieldDefs
	if fields == nil {
		fields = []string{}
	}
	tags := meta.Tags
	if tags == nil {
		tags = []string{}
	}
	return Dimension{
		ID:          id,
		Label:       meta.Title,
		Fields:      fields,
		Tags:        tags,
		Description: meta.Description,
	}, true
}

func parseVariables(raw map[string]json.RawMessage) []Variable {
	if raw["qEntryList"] == nil {
		return []Variable{}
	}
	var entries []struct {
		QInfo struct {
			QID string `json:"qId"`
		} `json:"qInfo"`
		QData struct {
			QName    string          `json:"qName"`
			QComment string          `json:"qComment"`
			QValue   json.RawMessage `json:"qValue"`
		} `json:"qData"`
	}
	if err := json.Unmarshal(raw["qEntryList"], &entries); err != nil {
		return []Variable{}
	}
	vars := make([]Variable, 0, len(entries))
	for _, e := range entries {
		vars = append(vars, Variable{
			ID:      e.QInfo.QID,
			Name:    e.QData.QName,
			Comment: e.QData.QComment,
			Value:   e.QData.QValue,
		})
	}
	return vars
}
