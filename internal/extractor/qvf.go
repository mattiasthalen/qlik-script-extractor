package extractor

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/mattiasthalen/qlik-parser/internal/redact"
)

// QVFData holds all artifacts extracted from a single .qvf file.
type QVFData struct {
	Script     string
	Measures   []Measure
	Dimensions []Dimension
	Variables  []Variable
	Sheets     []Sheet
	Lineage    ScriptLineage
	Warnings   []string
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

// maxBlockBytes caps how much a single candidate zlib stream is inflated.
// Metadata blocks (script, master items, sheet/object trees) are small; a
// larger stream is a data/symbol table we deliberately skip. The cap also
// bounds memory when scanning untrusted, multi-gigabyte files.
const maxBlockBytes = 16 << 20 // 16 MiB

// zlibFlags are the valid second header bytes of a zlib stream: the values
// where (0x78<<8 | flag) % 31 == 0.
var zlibFlags = map[byte]bool{0x01: true, 0x5E: true, 0x9C: true, 0xDA: true}

// qvfParser accumulates artifacts across the scan of one file, independent of
// whether the bytes come from memory or a ReaderAt.
type qvfParser struct {
	result *QVFData
	sheets map[string]*sheetRaw
	viz    map[string]Visualization
}

func newQVFParser() *qvfParser {
	return &qvfParser{
		result: &QVFData{
			Measures:   []Measure{},
			Dimensions: []Dimension{},
			Variables:  []Variable{},
			Sheets:     []Sheet{},
		},
		// Sheets reference object blocks by ID; both may appear anywhere in the
		// file and in any order, so collect them and resolve after the scan.
		sheets: map[string]*sheetRaw{},
		viz:    map[string]Visualization{},
	}
}

// feedBlock classifies one inflated, NUL-trimmed JSON block.
func (p *qvfParser) feedBlock(trimmed []byte) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(trimmed, &raw) != nil {
		return
	}
	// Master items, the script and variables are stored as bare top-level
	// blocks. Sheets and their chart objects are instead wrapped in a
	// qRoot/qProperty/qChildren envelope, so classify the block itself and,
	// if present, every property tree nested under qRoot.
	classifyBlock(raw, p.result, p.sheets, p.viz)
	if rootRaw, ok := raw["qRoot"]; ok {
		for _, prop := range collectObjectProps(rootRaw) {
			classifyBlock(prop, p.result, p.sheets, p.viz)
		}
	}
}

// finalize resolves cross-references and derives lineage and warnings.
func (p *qvfParser) finalize() *QVFData {
	resolveExtends(p.viz)
	p.result.Sheets = resolveSheets(p.sheets, p.viz)
	p.result.Lineage = ParseScriptLineage(p.result.Script)
	// Non-destructive by default: flag embedded secrets as warnings but leave
	// the extracted script intact. The service layer can opt into redaction.
	p.result.Warnings = redact.Warnings("script", redact.Scan(p.result.Script))
	return p.result
}

// ParseQVF reads a .qvf file and extracts all known artifact types in a single pass.
// It never returns NoScriptError; the Script field is simply empty if not found.
func ParseQVF(path string) (*QVFData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	p := newQVFParser()
	for i := 0; i < len(data)-1; i++ {
		if data[i] != 0x78 || !zlibFlags[data[i+1]] {
			continue
		}
		if block := inflateBounded(bytes.NewReader(data[i:])); block != nil {
			p.feedBlock(block)
		}
	}
	return p.finalize(), nil
}

// ParseReaderAt extracts artifacts from a .qvf exposed as an io.ReaderAt of the
// given size, without loading the whole file into the heap. Backing the
// ReaderAt with a memory-mapped file (e.g. golang.org/x/exp/mmap) lets the OS
// page multi-gigabyte apps in and out while the same per-block inflate cap
// bounds peak memory. The parsing core stays standard-library only.
func ParseReaderAt(ra io.ReaderAt, size int64) (*QVFData, error) {
	p := newQVFParser()

	const window = 1 << 20 // 1 MiB scan window
	buf := make([]byte, window+1)
	var off int64
	for off < size-1 {
		n, err := ra.ReadAt(buf, off)
		if n < 2 {
			if err != nil {
				break
			}
			off += int64(n)
			continue
		}
		for j := 0; j < n-1; j++ {
			if buf[j] != 0x78 || !zlibFlags[buf[j+1]] {
				continue
			}
			abs := off + int64(j)
			sr := io.NewSectionReader(ra, abs, size-abs)
			if block := inflateBounded(sr); block != nil {
				p.feedBlock(block)
			}
		}
		if err != nil {
			break // reached EOF (short final read)
		}
		// Overlap by one byte so a header straddling the window edge is caught.
		off += int64(n - 1)
	}
	return p.finalize(), nil
}

// inflateBounded decompresses a single zlib stream from r, bounded by
// maxBlockBytes, and returns the NUL-trimmed bytes. It returns nil for a false
// header match or an oversized (non-metadata) block.
func inflateBounded(r io.Reader) []byte {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, maxBlockBytes+1))
	if err != nil {
		return nil
	}
	if int64(len(out)) > maxBlockBytes {
		return nil
	}
	return bytes.TrimRight(out, "\x00")
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
