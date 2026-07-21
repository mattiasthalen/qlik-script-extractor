package extractor

import (
	"regexp"
	"strings"
)

// ScriptLineage is the structured lineage extracted from a load script:
// external connections, and the tables built by the script with their sources,
// operations and fields.
type ScriptLineage struct {
	Connections []ScriptConnection `json:"connections"`
	Tables      []ScriptTable      `json:"tables"`
}

// ScriptConnection is an external data connection referenced by the script.
type ScriptConnection struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // LIB, ODBC, OLEDB, CUSTOM, CONNECT
}

// ScriptTable is one table-building statement (a LOAD/SELECT, optionally with a
// JOIN/CONCATENATE/MAPPING prefix) and where its data comes from.
type ScriptTable struct {
	Name       string   `json:"name"`
	Operation  string   `json:"operation"`  // load, join, left join, right join, inner join, outer join, concatenate, mapping
	SourceType string   `json:"sourceType"` // qvd, file, resident, inline, sql, rest, autogenerate, other
	Source     string   `json:"source,omitempty"`
	Fields     []string `json:"fields"`
}

var (
	reLabel     = regexp.MustCompile(`^\s*(\[[^\]]+\]|"[^"]+"|` + "`" + `[^` + "`" + `]+` + "`" + `|[A-Za-z_][A-Za-z0-9_.]*)\s*:`)
	reConnect   = regexp.MustCompile(`(?i)^\s*(LIB|ODBC|OLEDB|CUSTOM)?\s*CONNECT\s+(?:32\s+|64\s+)?TO\s+(.*)$`)
	reResident  = regexp.MustCompile(`(?i)\bRESIDENT\s+(\[[^\]]+\]|"[^"]+"|[A-Za-z_][A-Za-z0-9_.$]*)`)
	reFromToken = regexp.MustCompile(`(?i)\bFROM\s+(\[[^\]]+\]|"[^"]+"|'[^']+'|[^\s(;,]+)`)
	reOpPrefix  = regexp.MustCompile(`(?i)^\s*((?:LEFT|RIGHT|INNER|OUTER)\s+JOIN|JOIN|CONCATENATE|MAPPING|NOCONCATENATE|ADD|REPLACE|BUFFER|CROSSTABLE)\b`)
	reParenName = regexp.MustCompile(`^\s*\(\s*(\[[^\]]+\]|"[^"]+"|[A-Za-z_][A-Za-z0-9_.$]*)`)
)

// ParseScriptLineage parses a Qlik load script into structured lineage. It is a
// best-effort, engine-free heuristic parser: it recognises the common statement
// shapes and records what it can, never failing on the parts it cannot model.
func ParseScriptLineage(script string) ScriptLineage {
	lineage := ScriptLineage{Connections: []ScriptConnection{}, Tables: []ScriptTable{}}
	if strings.TrimSpace(script) == "" {
		return lineage
	}

	for _, stmt := range splitStatements(script) {
		body := strings.TrimSpace(stmt)
		if body == "" {
			continue
		}
		upper := strings.ToUpper(body)
		if strings.HasPrefix(upper, "REM ") || strings.HasPrefix(upper, "REM\t") {
			continue
		}

		if c, ok := parseConnect(body); ok {
			lineage.Connections = append(lineage.Connections, c)
			continue
		}

		if tbl, ok := parseTableStatement(body); ok {
			lineage.Tables = append(lineage.Tables, tbl)
		}
	}
	return lineage
}

func parseConnect(body string) (ScriptConnection, bool) {
	m := reConnect.FindStringSubmatch(body)
	if m == nil {
		return ScriptConnection{}, false
	}
	kind := strings.ToUpper(strings.TrimSpace(m[1]))
	if kind == "" {
		kind = "CONNECT"
	}
	return ScriptConnection{Name: unquote(strings.TrimSpace(m[2])), Kind: kind}, true
}

func parseTableStatement(body string) (ScriptTable, bool) {
	tbl := ScriptTable{Operation: "load", Fields: []string{}}

	// Optional leading table label.
	if m := reLabel.FindStringSubmatch(body); m != nil {
		tbl.Name = unbracket(m[1])
		body = strings.TrimSpace(body[len(m[0]):])
	}

	// Optional JOIN / CONCATENATE / MAPPING prefix, possibly with (Target).
	if m := reOpPrefix.FindStringSubmatch(body); m != nil {
		tbl.Operation = strings.ToLower(collapseSpaces(m[1]))
		rest := strings.TrimSpace(body[len(m[0]):])
		if pm := reParenName.FindStringSubmatch(rest); pm != nil {
			tbl.Name = unbracket(pm[1])
		}
	}

	upper := strings.ToUpper(body)
	loadIdx := indexKeyword(upper, "LOAD")
	selectIdx := indexKeyword(upper, "SELECT")
	if loadIdx < 0 && selectIdx < 0 {
		return ScriptTable{}, false // not a table-building statement
	}

	classifySource(body, upper, &tbl)
	tbl.Fields = extractFields(body, upper, loadIdx, selectIdx, &tbl)
	return tbl, true
}

// classifySource determines where a LOAD/SELECT reads from and fills in the
// SourceType/Source fields.
func classifySource(body, upper string, tbl *ScriptTable) {
	switch {
	case indexKeyword(upper, "INLINE") >= 0:
		tbl.SourceType = "inline"
	case reResident.MatchString(body):
		tbl.SourceType = "resident"
		tbl.Source = unbracket(reResident.FindStringSubmatch(body)[1])
	case indexKeyword(upper, "AUTOGENERATE") >= 0:
		tbl.SourceType = "autogenerate"
	case indexKeyword(upper, "SELECT") >= 0 && indexKeyword(upper, "SQL") >= 0:
		// REST-connector loads look like `SQL SELECT ... FROM JSON ...`; the
		// JSON/XML target marks them as REST rather than a relational source.
		if m := reFromToken.FindStringSubmatch(body); m != nil {
			target := strings.ToUpper(m[1])
			if target == "JSON" || target == "XML" {
				tbl.SourceType = "rest"
			} else {
				tbl.SourceType = "sql"
				tbl.Source = unquote(unbracket(m[1]))
			}
		} else {
			tbl.SourceType = "sql"
		}
	case reFromToken.MatchString(body):
		m := reFromToken.FindStringSubmatch(body)
		src := m[1]
		srcUpper := strings.ToUpper(src)
		switch {
		case srcUpper == "JSON" || srcUpper == "XML":
			tbl.SourceType = "rest"
		case strings.Contains(strings.ToUpper(body), "(QVD") || strings.HasSuffix(strings.ToUpper(unbracket(src)), ".QVD"):
			tbl.SourceType = "qvd"
			tbl.Source = unbracket(src)
		default:
			tbl.SourceType = "file"
			tbl.Source = unquote(unbracket(src))
		}
	default:
		tbl.SourceType = "other"
	}

	// A load with no explicit label defaults its table name from the source.
	if tbl.Name == "" {
		tbl.Name = deriveName(tbl.Source)
	}
}

// extractFields pulls the field/alias list of a LOAD or SELECT statement.
func extractFields(body, upper string, loadIdx, selectIdx int, tbl *ScriptTable) []string {
	// Inline field lists live inside the data block header; skip them.
	if tbl.SourceType == "inline" {
		return []string{}
	}

	start, kw := loadIdx, "LOAD"
	if start < 0 || (selectIdx >= 0 && selectIdx < start) {
		start, kw = selectIdx, "SELECT"
	}
	if start < 0 {
		return []string{}
	}
	fieldsPart := body[start+len(kw):]
	fpUpper := upper[start+len(kw):]

	// Cut at the first source/clause keyword.
	end := len(fieldsPart)
	for _, kw := range []string{"FROM", "RESIDENT", "INLINE", "AUTOGENERATE", "WHERE", "GROUP BY", "WHILE", "ORDER BY"} {
		if idx := indexKeyword(fpUpper, kw); idx >= 0 && idx < end {
			end = idx
		}
	}
	fieldsPart = fieldsPart[:end]

	fields := []string{}
	for _, part := range splitTopLevel(fieldsPart, ',') {
		if name := fieldName(part); name != "" {
			fields = append(fields, name)
		}
	}
	return fields
}

// fieldName reduces a select-list expression to its resulting field name,
// preferring an "as" alias.
func fieldName(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}
	if part == "*" {
		return "*"
	}
	if alias := aliasAfterAs(part); alias != "" {
		return unbracket(alias)
	}
	// No alias: take the trailing identifier if it is a bare field reference.
	if !strings.ContainsAny(part, "()") {
		return unbracket(part)
	}
	return unbracket(part)
}

func aliasAfterAs(part string) string {
	up := strings.ToUpper(part)
	for i := 0; i+4 <= len(up); i++ {
		if up[i:i+4] == " AS " && !insideBrackets(part, i) {
			return strings.TrimSpace(part[i+4:])
		}
	}
	return ""
}

// --- lexical helpers --------------------------------------------------------

// splitStatements splits a script into statements on top-level semicolons,
// stripping // and /* */ comments while respecting quotes and [ ] brackets
// (so that lib:// paths and quoted text are preserved).
func splitStatements(script string) []string {
	var stmts []string
	var cur strings.Builder
	inS, inD := false, false
	brDepth := 0
	i := 0
	for i < len(script) {
		c := script[i]
		if !inS && !inD && brDepth == 0 {
			if c == '/' && i+1 < len(script) && script[i+1] == '/' {
				for i < len(script) && script[i] != '\n' {
					i++
				}
				continue
			}
			if c == '/' && i+1 < len(script) && script[i+1] == '*' {
				i += 2
				for i+1 < len(script) && !(script[i] == '*' && script[i+1] == '/') {
					i++
				}
				i += 2
				continue
			}
		}
		switch {
		case inS:
			if c == '\'' {
				inS = false
			}
		case inD:
			if c == '"' {
				inD = false
			}
		case brDepth > 0:
			if c == '[' {
				brDepth++
			} else if c == ']' {
				brDepth--
			}
		default:
			switch c {
			case '\'':
				inS = true
			case '"':
				inD = true
			case '[':
				brDepth++
			case ';':
				stmts = append(stmts, cur.String())
				cur.Reset()
				i++
				continue
			}
		}
		cur.WriteByte(c)
		i++
	}
	if strings.TrimSpace(cur.String()) != "" {
		stmts = append(stmts, cur.String())
	}
	return stmts
}

// splitTopLevel splits s on sep, ignoring separators inside quotes, ( ) or [ ].
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	inS, inD := false, false
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				inS = false
			}
		case inD:
			if c == '"' {
				inD = false
			}
		default:
			switch c {
			case '\'':
				inS = true
			case '"':
				inD = true
			case '(', '[':
				depth++
			case ')', ']':
				if depth > 0 {
					depth--
				}
			case sep:
				if depth == 0 {
					parts = append(parts, cur.String())
					cur.Reset()
					continue
				}
			}
		}
		cur.WriteByte(c)
	}
	parts = append(parts, cur.String())
	return parts
}

// indexKeyword finds kw in an already-uppercased string only where it appears
// as a whole word (not as a substring of a larger identifier).
func indexKeyword(upper, kw string) int {
	from := 0
	for {
		idx := strings.Index(upper[from:], kw)
		if idx < 0 {
			return -1
		}
		abs := from + idx
		before := abs == 0 || !isIdentByte(upper[abs-1])
		afterPos := abs + len(kw)
		after := afterPos >= len(upper) || !isIdentByte(upper[afterPos])
		if before && after {
			return abs
		}
		from = abs + len(kw)
	}
}

func insideBrackets(s string, pos int) bool {
	depth := 0
	for i := 0; i < pos && i < len(s); i++ {
		switch s[i] {
		case '[', '(':
			depth++
		case ']', ')':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth > 0
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func unbracket(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '[' && s[len(s)-1] == ']') ||
			(s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '`' && s[len(s)-1] == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"')) {
		return s[1 : len(s)-1]
	}
	return unbracket(s)
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// deriveName produces a table name from a source path (its base, without
// directory or extension) when the statement carries no explicit label.
func deriveName(source string) string {
	if source == "" {
		return ""
	}
	s := source
	if i := strings.LastIndexAny(s, "/\\"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "."); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
