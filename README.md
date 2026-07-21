# qlik-parser

[![CI](https://github.com/mattiasthalen/qlik-parser/actions/workflows/ci.yml/badge.svg)](https://github.com/mattiasthalen/qlik-parser/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/mattiasthalen/qlik-parser)](https://github.com/mattiasthalen/qlik-parser/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Extract artifacts from QlikView (.qvw) and Qlik Sense (.qvf) files.

## Quick Start

```sh
qlik-parser extract --source ./qlik-apps --out ./scripts
```

This scans `./qlik-apps` recursively for `.qvw` and `.qvf` files and writes extracted artifacts to `./scripts`, creating a folder per source file (e.g. `./scripts/sales.qvf/script.qvs`).

## Installation

Download the binary for your platform from the [Releases page](https://github.com/mattiasthalen/qlik-parser/releases/latest).

| Platform | Archive |
|----------|---------|
| Linux (amd64 / arm64) | `.tar.gz` |
| macOS (amd64 / arm64) | `.tar.gz` |
| Windows (amd64 / arm64) | `.zip` |

**Linux / macOS:**

```sh
tar -xzf qlik-parser_<version>_<os>_<arch>.tar.gz
chmod +x qlik-parser
mv qlik-parser /usr/local/bin/   # or any directory on your PATH
```

**Windows:**

Extract the `.zip` and move `qlik-parser.exe` to a directory on your `PATH`.

## Usage

### `extract`

Recursively scans `--source` for `.qvw` and `.qvf` files and extracts embedded artifacts.

```
qlik-parser extract [flags]
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--script` | | `false` | Extract load scripts |
| `--measures` | | `false` | Extract master measures (QVF only) |
| `--dimensions` | | `false` | Extract master dimensions (QVF only) |
| `--variables` | | `false` | Extract variables (QVF only) |
| `--sheets` | | `false` | Extract sheets & visualisations inventory (QVF only) |
| `--lineage` | | `false` | Extract load-script lineage: connections, tables, sources (QVF only) |
| `--source` | `-s` | current directory | Source directory to scan |
| `--out` | `-o` | alongside source files | Output directory |
| `--dry-run` | | `false` | Preview without writing files |

> No artifact flags passed → all artifacts extracted. Explicit flags → only those artifact types.

**Output path behaviour:**

- `--out` specified: mirrors source folder structure under the output directory, one folder per source file
- `--out` omitted: creates a folder per source file alongside the source

**Example — dry run:**

```sh
qlik-parser extract --source ./qlik-apps --dry-run
```

### `catalog`

Scans `--source` recursively for `.qvf` files and builds a combined cross-app
index of every master measure, master dimension and variable, noting which app
defines each so duplicated and conflicting definitions surface.

```
qlik-parser catalog [flags]
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--source` | `-s` | current directory | Source directory to scan |
| `--out` | `-o` | stdout | Output file |
| `--ndjson` | | `false` | Emit newline-delimited JSON rows for BigQuery loading |

An entry is marked `conflicting` when the same name resolves to more than one
distinct definition across apps. The `--ndjson` output is ready for
`bq load --source_format=NEWLINE_DELIMITED_JSON`.

### `serve` (Cloud Run documentation service)

Runs an HTTP service that documents Qlik apps from Cloud Storage. It reads the
`.qvf` via a memory-mapped `ReaderAt` so multi-gigabyte apps are paged by the OS
rather than loaded into RAM, and the parsing core stays standard-library only.

```
qlik-parser serve [--addr :8080]
```

**Endpoints:**

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/health` | none | Liveness/readiness probe. |
| `POST` | `/parse` | API key | Document one app. |
| `POST` | `/events` | Cloud Run IAM | Eventarc GCS object-finalize handler. |

`POST /parse` body (the caller passes a **location**, not the file body — QVFs
are large):

```json
{
  "source": "gs://my-bucket/apps/Sales.qvf",
  "output": "gs://my-bucket/docs",
  "redaction": "redact",
  "markdown": true,
  "inline": false
}
```

- `source` — `gs://…`, an http(s) signed URL, or a local path.
- `output` — `gs://` prefix or local dir for `<app>.json` (and `<app>.md`); omit to skip writing.
- `redaction` — `flag` (default, report only) or `redact` (replace secret values).
- `markdown` — request the AI documentation stage (only if enabled).
- `inline` — include the full document in the HTTP response.

**Configuration (environment):**

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Listen port (set by Cloud Run). |
| `QVF_API_KEY` | _(unset)_ | Required key for `/parse`; unauthenticated if unset. |
| `QVF_API_KEY_HEADER` | `X-API-Key` | Header carrying the key. |
| `QVF_OUTPUT_BUCKET` | _(unset)_ | Default output prefix (used by `/events`). |
| `QVF_TMP_DIR` | system temp | Scratch dir for streaming remote inputs before mmap. |
| `QVF_AI_ENABLED` | `false` | `true` enables the AI documentation stage. |
| `QVF_AI_MODEL` | `claude-sonnet-5` | Anthropic model id. |
| `ANTHROPIC_API_KEY` | _(unset)_ | API key for the AI stage. |

The API-key header is used because the Power Platform caller cannot easily mint
Google identity tokens. The AI stage is a separate switch, so extraction still
works with it off. The output document schema is documented in
[`docs/schema.md`](docs/schema.md).

**Deploy to Cloud Run:**

```sh
gcloud run deploy qvf-docs \
  --source . \
  --region europe-north1 \
  --no-allow-unauthenticated \
  --memory 2Gi \
  --set-env-vars QVF_API_KEY=<key>,QVF_OUTPUT_BUCKET=gs://my-bucket/docs

# Optional: enable the AI stage
gcloud run services update qvf-docs \
  --region europe-north1 \
  --set-env-vars QVF_AI_ENABLED=true,ANTHROPIC_API_KEY=<anthropic-key>
```

To trigger on new uploads, point an Eventarc GCS `object.finalized` trigger at
the service's `/events` path:

```sh
gcloud eventarc triggers create qvf-on-upload \
  --location europe-north1 \
  --destination-run-service qvf-docs \
  --destination-run-path /events \
  --event-filters "type=google.cloud.storage.object.v1.finalized" \
  --event-filters "bucket=my-upload-bucket" \
  --service-account <sa>@<project>.iam.gserviceaccount.com
```

### `version`

```sh
qlik-parser version
```

Prints the current version, e.g. `qlik-parser v0.1.0`.

### Global flags

| Flag | Default | Description |
|------|---------|-------------|
| `--log-level` | `disabled` | Log level: `debug`, `info`, `warn`, `error`, `disabled` |
