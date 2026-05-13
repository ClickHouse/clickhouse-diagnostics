# ClickHouse Diagnostic Tool

A Go-based diagnostic tool for ClickHouse that collects system information, runs a curated set of diagnostic queries, evaluates alert rules, and produces an HTML dashboard plus a shareable archive.

## Features

- **Per-environment query sets** — separate query directories for Cloud, on-prem, and government (hashed-PII) deployments, selected via `-mode`
- **Version-aware query execution** — automatically picks the highest-compatible query variant for the connected server
- **YAML-driven alerts** — drop a `.yaml` file in `alerts/` and the tool will run it, validate the SQL is read-only, and surface fired alerts in the dashboard
- **HTML dashboard** — single-file `dashboard.html` rendered into the output directory with key charts and alert results
- **Safe config collection** — passwords, tokens, and secrets are stripped from collected XML config files before they leave the host
- **Archive packaging** — bundles results, sanitised configs, alerts, and the dashboard into a single `tar.gz`

## Installation

### Prerequisites

- Go 1.23 or later (the module declares `go 1.23.9`)
- `make` (optional — convenience targets only; everything also works with plain `go` commands)
- Network access to a ClickHouse HTTP(S) interface
- Read access to the ClickHouse config directory (optional, only if collecting configs)

### Build with Make (recommended)

The repo ships with a `Makefile` that wraps the common workflows. From the repo root:

```bash
make deps        # go mod download && go mod tidy
make build       # produces ./bin/clickhouse-diagnostic
make run         # builds (if needed) and runs the binary interactively
make test        # go test -v ./...
make fmt         # go fmt ./...
make lint        # golangci-lint run  (requires golangci-lint installed)
make clean       # remove ./bin, ./clickhouse_results, ./configuration, *.tar.gz
make help        # list every target
```

### Build with `go` directly

If you don't want to use `make`:

```bash
# Fetch and tidy dependencies
go mod download
go mod tidy

# Build a binary at ./clickhouse-diagnostic
go build -o clickhouse-diagnostic ./cmd

# Or install into $GOPATH/bin (or $GOBIN) so it's on your PATH
go install ./cmd
```

> The examples below use `./clickhouse-diagnostic` for brevity. If you built via `make`, the binary is at `./bin/clickhouse-diagnostic` — either invoke that path directly or symlink it.

### Cross-platform release builds

`make release` produces binaries for the platforms we ship:

```bash
make release
# Output (in ./bin/):
#   clickhouse-diagnostic-linux-amd64
#   clickhouse-diagnostic-darwin-amd64
#   clickhouse-diagnostic-darwin-arm64
#   clickhouse-diagnostic-windows-amd64.exe
```

To cross-compile for a single target without `make`:

```bash
GOOS=linux  GOARCH=amd64 go build -o bin/clickhouse-diagnostic-linux-amd64  ./cmd
GOOS=darwin GOARCH=arm64 go build -o bin/clickhouse-diagnostic-darwin-arm64 ./cmd
```

Builds are statically linked Go binaries — copy the file to the target host and run, no runtime dependencies required.

## Usage

### Interactive run

```bash
./clickhouse-diagnostic
```

You will be prompted for any value not supplied on the command line:

| Prompt | Default |
|---|---|
| Protocol (http/https) | `http` |
| ClickHouse host | `localhost` |
| ClickHouse port | `8123` (http) / `8443` (https) |
| Username | _empty_ |
| Password (hidden) | _empty_ |
| Mode (cloud/onprem/gov) | `onprem` |
| Config directory | `/etc/clickhouse-server/config.d/` |

### Command-line flags

```
-host string           ClickHouse host
-port string           ClickHouse port
-user string           Username
-password string       Password (prefer the interactive prompt)
-protocol string       http or https
-mode string           Query mode: cloud, onprem, or gov (default "onprem")
-output-dir string     Where to write results (default "./clickhouse_results")
-config-dir string     ClickHouse config directory to collect
                       (default "/etc/clickhouse-server/config.d/")
-alerts-dir string     Directory with alert YAML rule files (default "./alerts")
-skip-config           Do not collect configuration files
-skip-alerts           Do not evaluate alert rules
-skip-dashboard        Do not generate dashboard.html
-skip-archive          Do not create the tar.gz archive
```

### Examples

Run against a Cloud cluster, no config collection (configs aren't accessible in Cloud):

```bash
./clickhouse-diagnostic -mode cloud -host my-service.us-east-1.aws.clickhouse.cloud \
  -port 8443 -protocol https -user default -skip-config
```

Run against an on-prem node, write everything to a custom directory:

```bash
./clickhouse-diagnostic -mode onprem -host prod-ch-01 \
  -output-dir ./diagnostics-2026-05-13
```

Run a quick check with only queries (no alerts, no dashboard, no archive):

```bash
./clickhouse-diagnostic -mode onprem -host localhost \
  -skip-alerts -skip-dashboard -skip-archive
```

Government / hashed-PII mode with a non-default alerts directory:

```bash
./clickhouse-diagnostic -mode gov -host gov-ch-01 \
  -alerts-dir ./alerts.gov
```

Fully non-interactive (CI / automation):

```bash
./clickhouse-diagnostic -mode cloud \
  -protocol https -host $CH_HOST -port 8443 \
  -user $CH_USER -password $CH_PASS \
  -skip-config
```

> **Security:** prefer the interactive password prompt or an environment variable over `-password` — flags appear in `ps`/shell history.

## Modes and Query Layout

`-mode` selects which top-level query directory the tool reads from:

| Mode | Query directory | Notes |
|---|---|---|
| `cloud` | `queries.cloud/` | Uses `clusterAllReplicas(...)` to fan out across replicas |
| `onprem` | `queries.onprem/` | Single-node `system.*` references |
| `gov` | `queries.gov/` | Same shape as on-prem but PII columns are hashed |

Alert queries use the same mode — see [Alerts](#alerts).

### Version-specific queries

Inside each mode directory, you can override a query for a specific ClickHouse version by placing it in a subdirectory named `MAJOR.MINOR.PATCH.BUILD`. The tool picks the highest version ≤ the connected server.

```
queries.cloud/
├── system.parts.sql              # default (used if no version override matches)
├── 23.8.1.0/
│   └── system.parts.sql          # used for servers 23.8.1.0..23.10.x
├── 23.10.1.0/
│   └── system.parts.sql          # used for servers 23.10.1.0..23.11.x
├── 23.11.1.0/
└── 25.4.1.0/
```

Directories that don't parse as a version are skipped.

## Alerts

Alert rules are plain YAML files in `alerts/` (override with `-alerts-dir`). Each file defines one read-only SELECT query — if it returns any rows, the alert fires and the rows are surfaced in the dashboard. All alert SQL is validated before execution: only `SELECT` / `WITH` is accepted, anything else (`INSERT`, `ALTER`, `DROP`, …) is rejected and the rule is skipped.

### YAML schema

```yaml
name: mutation_running_too_long      # unique snake_case id
title: "Mutation running > 3h"       # human-readable title
severity: warning                    # critical | warning | info  (default warning)
description: |
  Multi-line explanation of what this alert means and what to do about it.
tags:
  - mutations
  - performance

query: |
  SELECT database, table, mutation_id,
         dateDiff('hour', create_time, now()) AS hours_running,
         parts_to_do
  FROM {sys.mutations}
  WHERE parts_to_do > 0
    AND is_killed = 0
    AND dateDiff('hour', create_time, now()) > 3
  ORDER BY hours_running DESC

message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h"
```

### `{sys.<table>}` placeholder

Use `{sys.<table>}` in the query — the evaluator rewrites it based on the run mode:

| Mode | `{sys.mutations}` expands to |
|---|---|
| `onprem`, `gov` | `system.mutations` |
| `cloud` | `clusterAllReplicas(default, system.mutations)` |

This is what lets the same alert work across single-node and Cloud deployments.

### Message templating

In `message:`, `{column_name}` is replaced with the value from each result row. One formatted message is produced per row, so an alert that returns 5 rows produces 5 messages in the dashboard.

### Adding a new alert

1. Drop a new `.yaml` file in `alerts/` following the schema above.
2. Reference system tables via `{sys.<table>}`, not hard-coded names.
3. Keep the query strictly read-only — the security validator will block anything else.
4. Run the tool with `-skip-archive -skip-config` for a fast feedback loop.

## Configuration Collection

When `-skip-config` is not set, the tool reads files from `-config-dir` (default `/etc/clickhouse-server/config.d/`) and writes sanitised copies into `./configuration/`.

### What gets sanitised

- `<password>`, `<password_sha256_hex>`, `<password_double_sha1_hex>`, `<password_sha1_hex>`
- `<secret>`, `<token>`
- XML attributes: `password="..."`, `secret="..."`, `token="..."`
- Credentials embedded in connection URLs

### What does **not** get sanitised

- Hostnames, IPs, cluster topology
- Database and table names
- Performance / resource settings
- Non-security configuration

Always review the contents of `./configuration/` before sharing the archive.

## Output Layout

A single run produces:

```
clickhouse_results/
└── clickhouse_backup_YYYYMMDD_HHMMSS/
    ├── system.parts_YYYYMMDD_HHMMSS.native        # one file per query
    ├── system.mutations_YYYYMMDD_HHMMSS.native
    ├── ...
    └── dashboard.html                              # unless -skip-dashboard
configuration/                                      # unless -skip-config
└── *.xml                                           # sanitised configs
clickhouse_backup_YYYYMMDD_HHMMSS.tar.gz            # unless -skip-archive
```

- **Query results**: ClickHouse `Native` format (`.native`)
- **Dashboard**: standalone `dashboard.html`, loads Chart.js from CDN
- **Archive**: `tar.gz` containing the per-run results directory and the `configuration/` directory

## Keeping Queries, Alerts, and Dashboard Up to Date

The `local/` directory (gitignored — never committed) contains two scripts that
analyse closed ClickHouse support issues and surface gaps: system tables that
appear repeatedly in engineer queries but are not yet covered by the
`queries.*` directories, recurring alert patterns, and potential new dashboard
sections.

Run the workflow every few days (or whenever a batch of issues closes) to stay
ahead of the most common field problems.

### Prerequisites

| Tool | Purpose |
|---|---|
| Node.js ≥ 18 | Run `fetch_issues.js` |
| Python 3 | Run `analyze_issues.py` |
| [GitHub CLI (`gh`)](https://cli.github.com/) | API access to the private issues repo |

### One-time setup

```bash
# 1. Authenticate the GitHub CLI
gh auth login

# 2. Authorize the token for SAML SSO (ClickHouse org)
#    Open the URL printed by the command above, or go to:
#    https://github.com/settings/tokens
#    Find your token → "Configure SSO" → "Authorize" for the ClickHouse org

# 3. Verify access
gh api /repos/ClickHouse/support-escalation/issues?per_page=1 | head -5
```

### Running the analysis

```bash
# Step 1 — fetch the 300 most-commented recent issues into /tmp/issues_detailed.json
#           (~5–10 minutes depending on API rate limits)
node local/fetch_issues.js

# Step 2 — analyse the data and print a gap report
python3 local/analyze_issues.py
```

The analysis prints:

- **Topic frequency** — how many issues touch each problem area (merges, memory, keeper, etc.)
- **System table usage** — which `system.*` tables appear in engineer SQL but are not yet in `queries.*`
- **Representative SQL** — one example query per uncovered table to guide writing the new file
- **Exception code ranking** — the most-mentioned error codes to inform new alert thresholds

### Acting on the results

| Finding | Action |
|---|---|
| New system table with ≥ 5 SQL references | Add `system.<table>.sql` to `queries.cloud/`, `queries.onprem/`, and `queries.gov/` (hash private fields in gov) |
| Exception code appearing in > 10 % of issues | Add or update an alert in `alerts/` |
| Recurring operational pattern (e.g. replica delay, disk fill) | Add a new section to `internal/dashboard/generator.go` |

### Adjusting the issue filter

The fetch script targets issues that are:
- Closed after **2024-10-09**
- Labelled **`assign:core`**
- Have **> 7 comments** (signals significant back-and-forth)

Edit the constants at the top of `local/fetch_issues.js` to change these
filters. The `EXISTING_TABLES` set in `local/analyze_issues.py` should be kept
in sync with the actual contents of `queries.*` so the gap report stays
accurate.

## Troubleshooting

**"Queries folder './queries.<mode>' does not exist"**
The mode you passed has no matching directory at the repo root. Check `-mode` matches one of `cloud`, `onprem`, `gov`.

**"Config directory does not exist"**
Use `-skip-config` or pass the correct path with `-config-dir`. Cloud users should always use `-skip-config`.

**"Connection refused" / timeouts**
Verify the server is reachable on the chosen `-protocol` + `-host` + `-port`. The default port differs by protocol (8123 vs 8443).

**"security: query must be SELECT or WITH" (alert blocked)**
An alert YAML has a non-read-only statement. Alert queries are restricted to `SELECT` / `WITH`; rewrite the query or remove the rule.

**Dashboard charts blank**
The generated `dashboard.html` loads Chart.js from a public CDN. Open it in an environment with internet access, or pre-fetch the script and inline it for offline use.

**"Permission denied" on config files**
You don't have read access to `-config-dir`. Either run with appropriate privileges or use `-skip-config`.
