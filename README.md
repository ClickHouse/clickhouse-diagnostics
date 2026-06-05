# ClickHouse Diagnostic Tool

A Go-based diagnostic tool for ClickHouse that collects system information, runs a curated set of diagnostic queries, evaluates alert rules, and produces an HTML dashboard plus a shareable archive.

## Features

- **Per-environment query sets** — separate query directories for Cloud, on-prem, and government (hashed-PII) deployments, selected via `-mode`
- **Version-aware query execution** — automatically picks the highest-compatible query variant for the connected server
- **YAML-driven alerts** — drop a `.yaml` file in `alerts/` and the tool will run it, validate the SQL is read-only, and surface fired alerts in the dashboard
- **Query analysis mode** — focus the collection on one `query_id` (or `normalized_query_hash`) and get a `query_analysis/` slice covering query_log, text_log, processors_profile_log, and a fast-vs-slow comparison
- **HTML dashboard** — single-file `dashboard.html` rendered into the output directory with key charts, alert results, and (when scoped) the query-analysis breakdown
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
-query-id string       Run query analysis focused on this query_id (UUID).
                       See "Query analysis mode" below.
-normalized-query-hash string
                       Run query analysis focused on this normalized_query_hash
                       (UInt64). Can be combined with --query-id, or used alone.
-from string           Time-window start for query analysis (RFC3339 or YYYY-MM-DD).
                       Defaults to "last 3 days" when --query-id is set, or
                       "last 7 days" when only --normalized-query-hash is set.
-to string             Time-window end for query analysis (default: now).
-analysis-dir string   Directory containing query-analysis SQL files
                       (default "./queries.query_analysis")
-dry-run               List every query that would be executed (with the
                       system tables each touches and a read-only
                       EXPLAIN ESTIMATE per SELECT) and exit. No results,
                       no archive. See "Dry-run mode" below.
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

## Dry-run mode

Security-conscious customers can pass `-dry-run` to see exactly which queries the tool would execute, against which tables, **without** any actual data collection:

```bash
./clickhouse-diagnostic -mode cloud -host my-service.region.aws.clickhouse.cloud \
  -port 8443 -protocol https -user default \
  -dry-run
```

What `-dry-run` does:

- Lists every SELECT that would be sent (file-based, alert rules, dashboard inline, query-analysis bundle)
- Tags each query with the `system.*` tables it touches
- Adds a read-only `EXPLAIN ESTIMATE` block under each SELECT — reports the rows/marks/parts the query **would** scan but reads no data parts. See [the EXPLAIN ESTIMATE docs](https://clickhouse.com/docs/sql-reference/statements/explain#explain-estimate).
- Renders empty estimates as `this table is empty` (the planner's confirmation that nothing matches the predicate)
- Skips `-skip-config` and `-skip-archive` automatically (no side-effect files)

What still reaches the server in dry-run:

| Call | Why |
|---|---|
| `SELECT version()` | Picks the right query variant for the server version |
| Pre-flight for `--query-id` / `--normalized-query-hash` | Derives the hash + event_time (or the slowest query_id) so the printed analysis SQL has real values, not unbound `{query_id}` markers |
| `EXPLAIN ESTIMATE <query>` per SELECT | Read-only metadata only |

Combine with the query-analysis flags to dry-run the focused bundle too:

```bash
./clickhouse-diagnostic -mode cloud -host ... \
  -dry-run \
  -normalized-query-hash 15477159632099527852
```

Sample block of the output:

```
[28]
    Tables: system.query_log
    SQL:
      SELECT ts, query_id, query_duration_ms, ...
      FROM system.query_log
      WHERE query_id = '30df7836-fe07-4f42-9d52-832a69abbb8b'
        AND event_time >= '2026-05-28 09:06:21'
        AND event_time <= '2026-06-04 09:06:21'
    EXPLAIN ESTIMATE:
      ┌─database─┬─table─────┬─parts─┬─rows─┬─marks─┐
      │ system   │ query_log │     2 │   14 │     2 │
      └──────────┴───────────┴───────┴──────┴───────┘
```

## Modes and Query Layout

`-mode` selects which top-level query directory the tool reads from:

| Mode | Query directory | Notes |
|---|---|---|
| `cloud` | `queries.cloud/` | Uses `clusterAllReplicas(...)` to fan out across replicas |
| `onprem` | `queries.onprem/` | Single-node `system.*` references |
| `gov` | `queries.gov/` | Same shape as on-prem but PII columns are hashed |

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

## Query analysis mode

When you already know **which** query is the problem — a specific `query_id` from a customer ticket or a `normalized_query_hash` from a slow-query rollup — the tool can collect a focused slice of `query_log`, `text_log`, and `processors_profile_log` so you can understand *why* it was slow without bringing back the whole system. The analysis runs **in addition to** the regular per-mode collection; it does not replace it.

### Invoking it

| You have | Pass | What you get |
|---|---|---|
| A specific `query_id` (from a ticket) | `--query-id <uuid>` | Tool auto-derives the `normalized_query_hash` from `system.query_log`, centres the time window on the query's `event_time`, and runs both single-id and group queries |
| A `normalized_query_hash` (from a dashboard) | `--normalized-query-hash <uint64>` | Tool auto-derives the **slowest** `query_id` for that hash within the window (so the single-id files — ProfileEvents, text_log, tables-referenced — are populated) and runs the full bundle |
| Both | both flags | Skips both pre-flight derivations; otherwise identical to passing either alone |
| A specific time window | `--from <RFC3339>` / `--to <RFC3339>` | Overrides the auto-derived window (RFC3339 or `YYYY-MM-DD` accepted) |

Works in all three modes (`cloud`, `onprem`, `gov`) — table references adapt the same way as the alert evaluator (`clusterAllReplicas(...)` in cloud, plain `system.*` elsewhere).

**Gov mode caveat**: the `query` and `tables` columns in `system.query_log` contain raw, *unhashed* SQL and table names. The standard gov-mode collection already exposes this; query-analysis does not change that surface. If your environment can't ship raw SQL out, skip the analysis flags in gov mode.

### What it collects

Eleven `.sql` files under `queries.query_analysis/`, each written to `<backup>/query_analysis/<name>_<ts>.native`:

**Single-query-id (need `--query-id` or one auto-derived from `--normalized-query-hash`)**

| File | What it answers |
|---|---|
| `query_details.sql` | The full `query_log` row — duration, memory, read rows, query text, exception, profile events |
| `profile_events.sql` | All `ProfileEvents` for this execution, sorted by value descending. *Most useful single artifact.* |
| `text_log_parts.sql` | Just the "Selected X/Y parts by partition key, Z marks…" and "Reading approx. N rows with M streams" log lines — answers "did we full-scan?" |
| `text_log_full.sql` | Every `text_log` row for the query (up to 5000) — fallback when the targeted slices don't show the issue |
| `tables_for_query.sql` | Current DDL + size for the tables the query touched (joined from `query_log.tables`) |

**Hash-group (need `--normalized-query-hash`, auto-derived from `--query-id`)**

| File | What it answers |
|---|---|
| `fast_slow_query_ids.sql` | The slowest and fastest `query_id` for this hash in the window — defines the comparison pair |
| `profile_events_compare.sql` | Side-by-side `ProfileEvents` for slow vs fast execution with `delta` and `percentage_diff` columns. *Most diagnostic query in the bundle.* |
| `hash_by_host.sql` | Per-hostname execution count, avg / p95 / max duration, memory, errors — surfaces "one node is slow" patterns |
| `hash_summary.sql` | Per-minute execution count (executions / succeeded / failed). Drives the "Executions per minute" stacked bar. |
| `failed_over_time.sql` | Failed-execution count per minute, split by exception code — feeds the "Failed queries per minute" stacked bar |
| `failed_queries.sql` | Per-table × per-error breakdown of failures (tables touched, error type, user, count, first/last seen, sample exception) |
| `executions_timeline.sql` | One row per individual execution of the hash (`LIMIT 10000`, most recent first), including `ProfileEvents['UserTimeMicroseconds']`. Drives all five per-execution scatter charts (duration / memory / CPU / read rows / read bytes). |

### Dashboard integration

When `--query-id` or `--normalized-query-hash` is set and `--skip-dashboard` is not, the generated `dashboard.html` includes a new **🔍 Query Analysis** section near the top of the nav.

**Focus header**: which `query_id` (user-supplied OR auto-derived slowest), which hash, time window, plus the focus execution's duration / memory / read-rows, plus a one-line comparison of the slow vs fast `query_id` durations (e.g. "slowest 2400 ms · fastest 50 ms → 48× slower").

**Query text card** (cloud / onprem only): the exact SQL of the focus execution, monospaced and scrollable. Hidden — and also stripped from the embedded JSON — in `gov` mode, because the query text contains the table names gov mode is otherwise hashing.

**Per-execution scatters** — five charts, one dot per individual query (up to 10 000 rows from `executions_timeline.sql`). Green dot = success, red cross = failure. Hover shows `query_id`, hostname, exception code, and the metric value in human units:

| Chart | Y axis | Notes |
|---|---|---|
| Per-execution duration | sec or min (adaptive at 200 s) | the "why was THIS execution slow" view |
| Per-execution memory usage | MiB / GiB / TiB (adaptive) | |
| Per-execution user CPU | sec | from `ProfileEvents['UserTimeMicroseconds']` |
| Per-execution read rows | rows | |
| Per-execution read bytes | MiB / GiB / TiB (adaptive) | |

**Count bars** — minute-bucketed (the one place per-execution doesn't apply, since the metric *is* "1"):

| Chart | X | Y |
|---|---|---|
| Executions per minute | event minute | count, stacked succeeded / failed |
| Failed queries per minute | event minute | count, stacked by exception code (`MEMORY_LIMIT_EXCEEDED (241)`, `TIMEOUT_EXCEEDED (159)`, …) |

**Single-execution row**:

- Top 30 `ProfileEvents` for the focus execution
- Fast vs slow `ProfileEvents` (top 30 by `|delta|`)

**Detail tables**:

- Per-host distribution (executions / durations / memory / errors per hostname)
- Failed queries breakdown (per table × error type × user)
- Tables referenced by the focus query (current DDL + size)
- "Selected X parts, Y marks" log lines for the slowest execution
- Full `text_log` for the slowest execution (scrollable)

The section is hidden when neither analysis flag is set.

### Example

```bash
# A customer ticket has a query_id that timed out. Look at it and how it
# compares to other recent runs of the same statement shape.
./clickhouse-diagnostic --mode cloud \
  --host my-cluster.clickhouse.cloud --port 8443 --protocol https --user default \
  --query-id 1bc3abaf-968f-4d4f-be3d-f77251b1ff0b \
  --skip-config -skip-archive
# → Pre-flight: query_id ... → normalized_query_hash 7769688026807387533 (event_time ...)
# → Query analysis: running 11 file(s) (window <event-time-centred 48h>)
# → 11 written, 0 skipped
# → dashboard.html now has a "Query Analysis" section
```

```bash
# A dashboard shows a particular hash regressing this week. Run the
# group-comparison only over the last 7 days; no individual query_id.
./clickhouse-diagnostic --mode onprem --host prod-ch-01 \
  --normalized-query-hash 7769688026807387533 \
  --from 2026-05-23 --to 2026-05-30
# → 4 group files written; 7 single-id files skipped (no query_id supplied)
```

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

In addition, when `--query-id` or `--normalized-query-hash` is set, a **🔍 Query Analysis** section appears near the top of the nav. See [Query analysis mode](#query-analysis-mode) for what it contains.

A sticky top nav at the page header lets you jump straight to any section. Sections that depend on cluster-specific or version-specific data (Crash Log, Cluster Nodes, Replicas Health, Async Inserts, Query Analysis) are hidden when there is nothing to show.

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
