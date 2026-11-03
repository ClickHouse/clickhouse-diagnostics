# Query analysis

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## Query analysis mode

When you already know **which** query is the problem — a specific `query_id` from a customer ticket or a `normalized_query_hash` from a slow-query rollup — the tool can collect a focused slice of `query_log`, `text_log`, and `processors_profile_log` so you can understand *why* it was slow without bringing back the whole system. The analysis runs **in addition to** the regular per-mode collection; it does not replace it.

> **Not available in gov mode.** Query-analysis output embeds raw query text, exception messages, identifiers and full DDL — exactly the values gov-mode hashing exists to protect, and they can't be meaningfully hashed inside freeform text. `--query-id` / `--normalized-query-hash` are therefore rejected when `-mode gov` is set.

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

Twelve `.sql` files under `queries.query_analysis/`, each written to `<backup>/query_analysis/<name>_<ts>.<ext>`:

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
# → Query analysis: running 12 file(s) (window <event-time-centred 48h>)
# → 12 written, 0 skipped
# → dashboard.html now has a "Query Analysis" section
```

```bash
# A dashboard shows a particular hash regressing this week. Run the
# group-comparison only over the last 7 days; no individual query_id.
./clickhouse-diagnostic --mode onprem --host prod-ch-01 \
  --normalized-query-hash 7769688026807387533 \
  --from 2026-05-23 --to 2026-05-30
# → 12 written, 0 skipped — the tool auto-derives a representative
#   query_id (the slowest execution in the window), so the single-id
#   files run too. If no execution exists in the window, the 5
#   single-id files are skipped instead (7 written, 5 skipped).
```
