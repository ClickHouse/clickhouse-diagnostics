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
| Gov-mode salt (hidden, `gov` mode only) | _empty_ |

### Command-line flags

Run `./clickhouse-diagnostic -help` to see the full list. Current flags:

```
-host string           ClickHouse Host
-port string           ClickHouse Port
-user string           Username
-password string       Password (not recommended for security reasons)
-protocol string       Protocol (http or https)
-mode string           Query mode (cloud, onprem, gov) (default "onprem")
-output-dir string     Directory for results output (default "./clickhouse_results")
-config-dir string     ClickHouse config directory to collect
                       (default: /etc/clickhouse-server/config.d/)
-alerts-dir string     Directory containing alert YAML rule files (default "./alerts")
-salt string           Gov-mode hashing salt (8–64 alphanumeric chars;
                       prompts interactively if empty; gov mode only)
-skip-config           Skip collecting configuration files
-skip-alerts           Skip evaluating alert rules
-skip-dashboard        Skip generating HTML dashboard
-skip-archive          Skip creating archive of results and configuration
```

Any flag left empty on the command line is prompted for interactively (except the `-skip-*` toggles and `-output-dir` / `-alerts-dir`, which use their defaults silently).

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
# Salt is prompted interactively (hidden input). To pass it explicitly:
./clickhouse-diagnostic -mode gov -host gov-ch-01 \
  -alerts-dir ./alerts.gov -salt myDeployment2026
```

> **Gov mode requires a salt** — see [Gov mode and hashed names](#gov-mode-and-hashed-names) for what it does and why you need it.

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

> ⚠️ **The `queries.<mode>/` directory MUST exist in the current working directory when you run the binary.** The tool resolves the queries folder as a path *relative to your CWD* (e.g. `./queries.onprem`), not relative to the binary itself. So if you ship the binary to another host, you must ship the matching `queries.<mode>/` folder alongside it and `cd` into the directory containing both before running.
>
> Recommended bundling pattern:
> ```bash
> # Build + bundle locally
> make release
> tar czf clickhouse-diagnostic.tgz \
>   bin/clickhouse-diagnostic-linux-amd64 \
>   queries.cloud queries.onprem queries.gov
>
> # On the target host
> scp clickhouse-diagnostic.tgz host:/tmp/
> ssh host
> mkdir -p /opt/ch-diag && cd /opt/ch-diag && tar xzf /tmp/clickhouse-diagnostic.tgz
> ./bin/clickhouse-diagnostic-linux-amd64 -mode onprem ...   # CWD now has the queries.* folders
> ```
>
> Running from `/`, `~`, or any other directory without `queries.<mode>/` as a sibling will fail with `Error: Queries folder './queries.<mode>' does not exist`.

Alert queries use the same mode — see [Alerts](#alerts).

### Gov mode and hashed names

In `gov` mode, every database and table name written to the support-bound output is replaced with `hex(SHA256(name + salt))`. The salt is supplied by **you** at runtime (via `-salt` or the interactive prompt) — the tool does **not** ship with a default. This is what makes the hashes meaningful: without a per-customer salt, anyone with the source could pre-compute hashes for common names like `users`, `events`, or `orders` and reverse the obfuscation.

**Salt requirements:**

- 8–64 ASCII alphanumeric characters (`A–Z`, `a–z`, `0–9`)
- No spaces, no punctuation, no quotes — keeps the value safe inside SQL string literals
- Pick something **not guessable from public information** (avoid your company name, deployment ID, or anything in your support tickets)
- **Re-use the same salt across runs** if you want hashes to be comparable over time

**What the tool produces in gov mode:**

```
clickhouse_results/
├── clickhouse_backup_YYYYMMDD_HHMMSS/                       # → goes into the archive
│   └── *.native                                             # hashed names inside
└── clickhouse_backup_YYYYMMDD_HHMMSS_gov_name_mapping.csv   # → stays LOCAL
```

The mapping CSV (real_name → hashed_name) is written **outside** the timestamped backup folder, so it is **never** picked up by `tar` when the archive is built. Keep it on your machine for your own correlation work — and confirm before sending the archive that the salt and the CSV did not accidentally land inside it.

**What to share with support:**

| File | Share with support? |
|---|---|
| `clickhouse_backup_*.tar.gz` | Yes |
| `clickhouse_backup_*_gov_name_mapping.csv` | **No — keep local** |
| The salt value itself | **No — keep local** |

If you lose the salt, the hashes in past archives are no longer reversible (you can still run a new diagnostic with a fresh salt — but old and new hashes won't compare).

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

### Bundled alert rules

The repo ships with 11 alert rules in `alerts/`. They are intended as a starting point — adjust thresholds to match your workload.

| Rule | Severity | Fires when |
|---|---|---|
| `crash_log_entries` | critical | `system.crash_log` is non-empty (server crashed at least once) |
| `replica_readonly` | critical | A replicated table is in read-only mode (lost Keeper session, disk full, network partition) |
| `replication_queue_errors` | critical | Replication queue entries have a non-empty `last_exception` |
| `disk_space_low` | critical | Any disk has less than 15% free space |
| `keeper_exception_spike` | warning | More than 20 KEEPER_EXCEPTION (code 999) errors in the last hour |
| `high_exception_rate` | warning | More than 50 query exceptions for a single exception code in the last hour |
| `too_many_simultaneous_queries` | warning | More than 10 code-252 errors in the last hour (`max_concurrent_queries` hit) |
| `too_many_parts` | warning | A partition has more than 300 active parts (CH throttles at 1000) |
| `large_parts` | warning | A single active part is larger than 150 GB |
| `mutation_running_too_long` | warning | A mutation has been running for more than 3 hours |
| `detached_parts_exist` | info | Parts exist in the `detached/` folder (failed merges, manual detach, replication conflicts) |

Every rule is a single `SELECT` against system tables; rows returned become alert instances in the dashboard. Open the YAML files directly to see the exact thresholds and tweak them.

## Dashboard

When `-skip-dashboard` is not set, the tool generates a single self-contained `dashboard.html` inside the per-run results folder. The dashboard embeds all query results inline as JSON and loads only Chart.js from a CDN, so it can be opened from disk (`file://`) on any machine with internet access.

### Sections

| # | Section | What it shows |
|---|---|---|
| 1 | 🚨 **Alert Summary** | Fired alerts grouped by severity (critical / warning / info), with the row-level message template expanded per instance. A green "no issues" banner appears when nothing fired. |
| 2 | 📈 **Overview** | Top-level counters: server version, uptime, total databases, total tables, active parts, total size |
| 3 | 📦 **Storage** | Size by database (horizontal bar), table-engine distribution (doughnut), and a top-20-by-size table list |
| 4 | 📋 **Tables Explorer** | Searchable / paginated table of every user table with engine, parts, rows, size, partition / sorting keys, and storage policy |
| 5 | 📊 **Query Activity** (last 7 days) | Queries per hour by kind (stacked bar), count by kind, average duration & memory by kind |
| 6 | 🔍 **Query Deep Dive** (last 7 days) | Top 20 slowest query patterns (by avg duration), top 20 heaviest reads (avg MB/query), per-user executions & errors, slow-query details and per-user summary tables |
| 7 | ⚠️ **Exceptions** (last 7 days) | Top exception codes by count, plus an exception-details table with the most recent message per code |
| 8 | 🔧 **Part Log Events** (last 7 days) | Part events per day by event type (stacked bar) and event-type distribution |
| 9 | 📖 **Dictionaries** | Status distribution (LOADED / FAILED / LOADING), bytes allocated per dictionary, lifetime configuration (min/max), and a details table including `last_exception` (gov mode emits an empty value here) |
| 10 | 💥 **Crash Log** | Recent entries from `system.crash_log` — section is hidden when the table is empty |
| 11 | ⏳ **Pending Operations** | Pending mutations and detached-parts tables side by side |
| 12 | 🔄 **Replication Queue** | Current entries in `system.replication_queue` with type, table, and last exception |
| 13 | 🌐 **Cluster Nodes** (cloud mode only) | Hosts in the `default` cluster with shard / replica / active status |
| 14 | 🔁 **Replicas Health** | Replication-delay distribution, queue-size by table, per-replica details (only shown when replicated tables exist) |
| 15 | 💾 **Disk Usage** | Free vs used space per disk plus a disk-details table |
| 16 | 🛑 **Server Error Counters** | Top 20 cumulative error codes from `system.errors`, high-part-count partitions (>100 parts → potential code-497 risk), and TTL activity from `part_log` |
| 17 | ⚡ **Async Insert Activity** (last 24 h) | Flush count per hour by status — section is hidden when `system.asynchronous_insert_log` is empty or in gov mode |

A sticky top nav at the page header lets you jump straight to any section. Sections that depend on cluster-specific or version-specific data (Crash Log, Cluster Nodes, Replicas Health, Async Inserts) are hidden when there is nothing to show.

### What's interactive vs static

- **Interactive**: Tables Explorer (full text search, database/engine filters, pagination); all charts (hover tooltips, legend toggling).
- **Static**: every other table — they render in a fixed order, but their underlying JSON is embedded in the page so you can `grep DATA dashboard.html | head` if you want raw values.

## Configuration Collection

When `-skip-config` is not set, the tool reads files from `-config-dir` (default `/etc/clickhouse-server/config.d/`) and writes sanitised copies into `./configuration/`.

Sanitisation runs in two layers — proper XML / YAML parsing first, then a heuristic byte-pattern pass over the result. If a file cannot be parsed, the tool **fails closed**: a warning is logged and the file is skipped from `./configuration/` rather than shipped un-sanitised.

### What gets sanitised

**Structural** (driven by tag / attribute / YAML-key name, case-insensitive):

- Any name matching the exact list: `client_id`, `tenant_id`, `account_name`, `account_key`, `storage_account_url`, `role_arn`, `role_session_name`, `session_token`, `connection_string`
- Any name **containing** one of: `password`, `passwd`, `secret`, `credential`, `token`, `private_key`, `api_key`, `access_key`, `service_account` — so future or custom tags like `<my_db_password>`, `<gcp_service_account_credentials>`, `<legacy_api_token>` are caught without code changes
- XML attribute values with the same naming rules: `password="..."`, `aws_access_key_id="..."`, etc.
- Multi-line values inside the above tags (the previous regex-only implementation missed these)

**Heuristic** (byte-shape signatures, applied to the whole file including comments and free text):

- PEM-encapsulated private keys (`-----BEGIN ... PRIVATE KEY-----` blocks)
- AWS access key IDs (`AKIA…`, `ASIA…`)
- JWT tokens (`eyJ…` three-segment shape)
- Bcrypt password hashes (`$2[abxy]$…`)
- Long hex strings (40+ chars — SHA-1, SHA-256, longer)
- Long base64 blobs (128+ chars — GCP service-account payloads etc.)
- Credentials embedded in URLs (`proto://user:pw@host` — only the password is redacted, host stays)
- `keyword: value` / `keyword = value` / `keyword "value"` disclosures in prose, where keyword contains `password`, `secret`, `token`, `api_key`, `access_key`, `private_key`, or `credential` — catches values pasted into XML comments or YAML `# notes`

### What does **not** get sanitised

- Hostnames, IPs, cluster topology
- Database and table names
- Performance / resource settings
- Non-security configuration
- Prose mentioning credential keywords without an actual value (e.g. `password requirements are documented elsewhere`)

The heuristic intentionally favours over-redaction over under-redaction — a stray flagged hash in the output is harmless, a leaked credential is not. Always review the contents of `./configuration/` before sharing the archive.

## Output Layout

A single run produces:

```
clickhouse_results/
├── clickhouse_backup_YYYYMMDD_HHMMSS/                       # → archived
│   ├── system.parts_YYYYMMDD_HHMMSS.native                  #   one file per query
│   ├── system.mutations_YYYYMMDD_HHMMSS.native
│   ├── ...
│   └── dashboard.html                                       #   unless -skip-dashboard
└── clickhouse_backup_YYYYMMDD_HHMMSS_gov_name_mapping.csv   # → LOCAL only (gov mode)
configuration/                                               # → archived (unless -skip-config)
└── *.xml                                                    #   sanitised configs
clickhouse_backup_YYYYMMDD_HHMMSS.tar.gz                     # unless -skip-archive
```

- **Query results**: ClickHouse `Native` format (`.native`)
- **Dashboard**: standalone `dashboard.html`, loads Chart.js from CDN
- **Archive**: `tar.gz` containing the per-run results directory and the `configuration/` directory
- **Gov-mode mapping CSV** (gov mode only): sits next to the backup folder, **not inside it** — never goes into the archive. See [Gov mode and hashed names](#gov-mode-and-hashed-names).

## Troubleshooting

**"Queries folder './queries.<mode>' does not exist"**
The mode you passed has no matching directory at the repo root. Check `-mode` matches one of `cloud`, `onprem`, `gov`.

**"Config directory does not exist"**
Use `-skip-config` or pass the correct path with `-config-dir`. Cloud users should always use `-skip-config`.

**"Connection refused" / timeouts**
Verify the server is reachable on the chosen `-protocol` + `-host` + `-port`. The default port differs by protocol (8123 vs 8443).

**"security: query must be SELECT or WITH" (alert blocked)**
An alert YAML has a non-read-only statement. Alert queries are restricted to `SELECT` / `WITH`; rewrite the query or remove the rule.

**"Invalid gov-mode salt: must be 8–64 ASCII alphanumeric characters"**
The `-salt` value (or interactive prompt input) contains spaces, punctuation, or is the wrong length. Pick a value that matches `[A-Za-z0-9]{8,64}` — see [Gov mode and hashed names](#gov-mode-and-hashed-names).

**Dashboard charts blank**
The generated `dashboard.html` loads Chart.js from a public CDN. Open it in an environment with internet access, or pre-fetch the script and inline it for offline use.

**"Permission denied" on config files**
You don't have read access to `-config-dir`. Either run with appropriate privileges or use `-skip-config`.
