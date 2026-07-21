# Output JSON schema

The QVF documentation service emits a single, versioned **Document** per app.
The schema version lives in `schemaVersion` (currently `1.0.0`). Minor bumps are
additive; a major bump signals a breaking change.

## Document

| Field | Type | Description |
|-------|------|-------------|
| `schemaVersion` | string | Schema version, e.g. `1.0.0`. |
| `source` | string | Input URI (`gs://…`, signed URL, or local path). |
| `app` | string | App name (source basename without extension). |
| `generatedAt` | string (RFC 3339) | UTC timestamp of generation. |
| `redaction` | string | `flag` or `redact` — how secrets were handled. |
| `script` | string | The load script (redacted when `redaction="redact"`). |
| `measures` | Measure[] | Master measures. |
| `dimensions` | Dimension[] | Master dimensions. |
| `variables` | Variable[] | Variables. |
| `sheets` | Sheet[] | Per-sheet inventory of visualisations. |
| `lineage` | ScriptLineage | Connections and table lineage from the script. |
| `warnings` | string[] | Detected secrets and processing warnings. |
| `markdown` | string (optional) | AI-generated docs, present only when the AI stage ran. |

## Measure

`id`, `label`, `def` (expression), `tags` (string[]), `description`.

## Dimension

`id`, `label`, `fields` (string[]), `tags` (string[]), `description`.

## Variable

`id`, `name`, `comment`, `value` (raw JSON — preserves the original type).

## Sheet

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Sheet object id. |
| `title` | string | Sheet title. |
| `description` | string | Sheet description. |
| `objects` | Visualization[] | Objects placed on the sheet. |

### Visualization

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Object id. |
| `type` | string | `barchart`, `table`, `kpi`, `pivot-table`, … |
| `title` | string (optional) | Object title (string or expression). |
| `dimensions` | VizField[] | Dimensions used. |
| `measures` | VizField[] | Measures used. |
| `masterObjectId` | string (optional) | Set when the object references a master visualisation. |
| `unclassified` | bool (optional) | `true` when the object shape was not recognised. |

### VizField

`label`, `def` (inline field/expression), `libraryId` (master-item reference).
Any subset may be present.

## ScriptLineage

| Field | Type | Description |
|-------|------|-------------|
| `connections` | ScriptConnection[] | `name`, `kind` (`LIB`/`ODBC`/`OLEDB`/`CUSTOM`/`CONNECT`). |
| `tables` | ScriptTable[] | One entry per LOAD/SELECT statement. |

### ScriptTable

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Table name (label or join target). |
| `operation` | string | `load`, `join`, `left join`, `concatenate`, `mapping`, … |
| `sourceType` | string | `qvd`, `file`, `resident`, `inline`, `sql`, `rest`, `autogenerate`, `other`. |
| `source` | string (optional) | Source path / resident table / SQL table. |
| `fields` | string[] | Loaded field/alias names. |

## Catalog (cross-app)

The `catalog` command emits a separate document with `apps`, `measures`,
`dimensions`, `variables` and `conflicts`. Each entry has a `name`, `kind`, a
`definitions` list (`definition` + the `apps` using it) and a `conflicting`
flag (true when one name has more than one distinct definition). `--ndjson`
flattens this to one `{kind,name,definition,app,conflicting}` row per line for
`bq load`.
