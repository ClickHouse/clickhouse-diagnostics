# Alerts

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## Alerts

Alert rules are plain YAML files in `alerts/` (override with `-alerts-dir`). Each rule names a **check** (`check: go:<name>`, a Go function in `internal/alert/checks_*.go`) that is evaluated over the files the collectors already wrote — the rules send **no query of their own**, so a run on a struggling server costs the collection and nothing more. If the check returns any rows, the alert fires and the rows are surfaced in the dashboard. The rule's `query:` is the same test written as a read-only SELECT: it is what the dashboard hands the customer to run live, and the check mirrors it (hourly log buckets instead of raw rows — a window's oldest hour may reach up to an hour further back). All alert SQL is validated: only `SELECT` / `WITH` is accepted, anything else (`INSERT`, `ALTER`, `DROP`, …) breaks the rule.

A rule without `check:` still runs its `query:` against the server, as before — the way to try a new rule before writing its check. So do all rules when `-output-format native` or `tsv` is used: the checks read the JSONEachRow files.

### Rule outcomes

Every run reports four distinct outcomes, because each means something different to whoever reads the archive:

| Outcome | Meaning |
|---|---|
| **fired** | The rule ran and matched rows — a real finding. |
| **clean** | The rule ran and matched nothing. |
| **errored** | The rule could not run (bad SQL, a column missing on this version, no `SELECT` grant). **Not a finding** — counted and displayed separately, and excluded from "checked". |
| **not applicable** (skipped) | A file the check reads was not collected (skipped in an essential run, without a Keeper session, past the time budget, or failed — the collector and the reason are named), or the system table doesn't exist here — `crash_log` on a healthy self-managed instance, or a config-disabled `text_log`/`query_log` — or the rule declares `modes:` and this run is in another mode (a per-replica comparison that needs the cloud fan-out, a replication-queue rule on SharedMergeTree). Excluded from "checked" so it never reads as a check that passed; the reason is recorded per rule. |

```
Alert evaluation complete: 8 rule(s) checked, 1 fired, 2 errored, 1 not applicable
```

Only rules that actually produced an answer count as *checked*. The dashboard mirrors this: findings get a severity badge, errored rules get a muted **⚠ N Could not run** chip (never a red severity count), and skipped rules are listed as "not applicable" with their reason (table not present, or the rule's `modes:` exclude this run mode).

A missing **column** is always an error, never "not applicable" — that's the signal that a rule needs [version-gating](versions.md#version-specific-queries).

### `alerts_summary.json`

The HTML dashboard is the only other place alert results are recorded, so whenever it **isn't** produced the archive would otherwise contain no trace that the rules ran at all. In that case the tool writes `alerts_summary.json` into the output directory instead. It appears when:

- `-mode gov` (the dashboard is [withheld](modes.md#what-gov-mode-does-not-produce)),
- `-skip-dashboard` was passed, or
- dashboard generation failed.

It records each rule's name, title, severity, file (including the version-override directory, e.g. `24.1.1.0/…`), outcome (`fired` / `clean` / `error` / `skipped`) and **instance count**, plus the run totals. **Matched rows are never included in any mode** — their columns are defined per-rule, so they routinely carry database and table names. The count is the substitute: *"`too_many_parts` fired, 12 instances"*.

> **Cloud caveat.** In `cloud` mode per-replica tables are read through `clusterAllReplicas`, so an `UNKNOWN_TABLE` can mean *absent everywhere* or *present on only some replicas* — and `system.crash_log` exists only on the node that crashed. The tool therefore counts how many replicas actually have the table before deciding: zero means genuinely not applicable, anything else (or an unverifiable answer) surfaces the error rather than risk hiding a real crash.

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
check: go:mutation_running_too_long # the Go check evaluated over the collected files
source:                              # the collector files it reads (documentation; a test
  - system.mutations                 # requires every one to be a shipped collector)
  - system.version
# modes: [cloud]                     # optional: run only in these modes (cloud | onprem | gov);
                                     # elsewhere the rule is reported as not applicable, not errored —
                                     # for queries that only make sense with a clusterAllReplicas
                                     # fan-out, or with a replication queue SharedMergeTree lacks

query: |
  SELECT database, table, mutation_id,
         dateDiff('hour', create_time, now()) AS hours_running,
         parts_to_do
  FROM {sys.mutations}
  WHERE parts_to_do > 0
    AND dateDiff('hour', create_time, now()) > 3
  ORDER BY hours_running DESC

message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h"

# investigate:                       # optional: follow-ups for the customer to run on their own
#   - title: "Mutations still running, per table"     # cluster when the rule fires — the
#     query: |                       # "which one?" a finding raises. Shown, never run by the tool;
#       SELECT database, table, count() FROM {sys.mutations}   # read-only SELECT, {sys.*} expanded
#       WHERE parts_to_do > 0 GROUP BY database, table          # like query.
```

In `dashboard.html`, every alert that fired, or whose check failed, carries a collapsed **queries to run on your cluster** line under its instances. Opening it shows the rule's `query` — `{sys.*}` already expanded for the bundle's mode, so it pastes into `clickhouse-client` on the same cluster — and the rule's `investigate:` follow-ups, each with a copy button. Rules that found nothing carry no SQL, and `alerts_summary.json` never does (it is counts only). On a cloud bundle the queries keep their `clusterAllReplicas(…)` form, which needs the same `REMOTE` grant the collection did.

### `{sys.<table>}` placeholder

Use `{sys.<table>}` in the query — the evaluator rewrites it based on the run mode:

| Mode | `{sys.query_log}` expands to |
|---|---|
| `onprem`, `gov` | `system.query_log` |
| `cloud` | `clusterAllReplicas(default, system.query_log)` |

This is what lets the same alert work across single-node and Cloud deployments. Tables whose rows are shared across replicas (`parts`, `tables`, `mutations`, `replicas`, `replication_queue`, `detached_parts`, `columns`, `databases`) are **not** wrapped even in cloud mode — `clusterAllReplicas` would duplicate their rows (see `internal/query/template.go`).

### Message templating

In `message:`, `{column_name}` is replaced with the value from each result row. One formatted message is produced per row, so an alert that returns 5 rows produces 5 messages in the dashboard.

### Adding a new alert

1. Drop a new `.yaml` file in `alerts/` following the schema above.
2. Reference system tables via `{sys.<table>}`, not hard-coded names.
3. Keep the query strictly read-only — the security validator will block anything else.
4. Run the tool with `-skip-archive -skip-config` for a fast feedback loop; without `check:` the query runs live.
5. Write the check: a `func(*bundle.Bundle, mode string) ([]map[string]interface{}, error)` registered with `RegisterCheck` in `internal/alert/checks_*.go`, returning the columns `message:` uses. If a column it needs is not collected yet, add it to the collector. Add fixture tests (`internal/alert/checks_fixture_test.go`) for just past and just short of each threshold, then set `check:` and `source:`.

### Bundled alert rules

The repo ships with 18 alert rules in `alerts/` (plus version-gated overrides in `alerts/<version>/`, and one rule that exists only there — `keeper_xid_renewal_due`, 24.3+). They are intended as a starting point — adjust thresholds to match your workload.

| Rule | Severity | Fires when |
|---|---|---|
| `crash_log_entries` | critical | `system.crash_log` is non-empty (server crashed at least once) |
| `replica_readonly` | critical | A replicated table is in read-only mode (lost Keeper session, disk full, network partition) |
| `replication_queue_errors` | critical | Replication queue entries have a non-empty `last_exception` |
| `disk_space_low` | critical | A disk that **holds user data** has less than 15% free space — **on any replica** in cloud mode, with the reporting host named in the message. A disk is counted when some user table's storage policy names it (`system.storage_policies` × `system.tables.storage_policy`); one that none does is not data capacity — on a managed service it is the local filesystem cache, sized to be filled, which otherwise fired this rule on a healthy service at 14.7% free. The filter is evidence, not a mode check, so a self-managed cluster collected in cloud mode still raises a critical for a hot local data disk. No user table with a policy = every disk judged |
| `keeper_health` | critical | The two-signal Keeper health test per hour over 7 days: more than 1000 `ZooKeeperHardwareExceptions` **and** `ZooKeeperTransactions` below 50 % of the 7-day median — Keeper effectively unavailable (low traffic alone never fires: an idle hour is not an outage); catches outages that never reached `query_log` |
| `keeper_connection_blips` | warning | More than 1000 `ZooKeeperHardwareExceptions` in an hour while Keeper traffic stayed at or above 50 % of usual (a positive 7-day median) — a session lost and re-established |
| `keeper_exception_spike` | warning | More than 20 KEEPER_EXCEPTION (code 999) errors in one hour of the last 24 hours (one instance per hour) |
| `high_exception_rate` | warning | More than 50 query exceptions for a single exception code in one hour of the last 24 hours (one instance per hour and code, worst 24) |
| `background_operation_failures` | warning | More than 50 failed merges / fetches / mutations with the same code in one hour of the last 24 (`part_log`) |
| `merges_stalled` | warning | An hour in the last 24 with more than 100 `NewPart` events and zero completed merges (`part_log`) — Keeper down, pool paused or every merge failing |
| `fetch_pool_saturated` | warning | Hourly average of `BackgroundFetchesPoolTask` at ≥ 90 % of `background_fetches_pool_size` in 3 of the last 24 hours, per replica — the fetch executor has no queue, so on SharedMergeTree this is parts-propagation lag |
| `schedule_pool_saturated` | warning | Hourly average of `BackgroundSchedulePoolTask` at ≥ 95 % of `background_schedule_pool_size` in 3 of the last 24 hours, per replica — every table's periodic tasks run here; tens of thousands of replicated tables pin it first |
| `parts_propagation_lag` | warning | *(cloud only — `modes: [cloud]`)* A replica is ≥ 32 blocks behind the newest replica on a partition, or has none of a partition written to in the last day, of a SharedMergeTree table — parts-propagation lag; one instance per replica with its worst partition, the full list in `system.parts_max_block_by_replica` |
| `too_many_simultaneous_queries` | warning | More than 10 code-202 (`TOO_MANY_SIMULTANEOUS_QUERIES`) errors in the last hour (`max_concurrent_queries` hit) |
| `too_many_parts` | warning | A partition has more than 300 active parts (inserts are delayed from `parts_to_delay_insert` = 1000 and rejected with code 252 `TOO_MANY_PARTS` at `parts_to_throw_insert` = 3000) |
| `large_parts` | warning | A single active part is larger than 150 GB |
| `mutation_running_too_long` | warning | A mutation has been running for more than 3 hours |
| `detached_parts_exist` | info | Parts exist in the `detached/` folder (failed merges, manual detach, replication conflicts) |
| `keeper_xid_renewal_due` | info | *(24.3+ only)* A Keeper session is within 24 h of the **32-bit** counter limit (2³¹) at the rate its host is measured to use, where that host has one live Keeper connection and at least 2 h of `metric_log` behind the estimate. Reported as headroom, not as a renewal that will happen: with `use_xid_64` enabled in `<zookeeper>` the counter is 64-bit and never wraps, and no system table exposes that setting — check `configuration/`. On a default 32-bit counter the renewal is a short burst of Keeper exceptions and a reconnect; investigate only if INSERTs failed in those minutes (`query_log`, or flushes in `asynchronous_insert_log`) |

Every rule is a Go check over the collected files, with the same test as a `SELECT` in `query:`; rows returned become alert instances in the dashboard. In cloud mode the Keeper rules read the per-host `metric_log_by_host_3_days`, so their "usual" median spans 3 days there, not 7. Rules that read a log table look back **24 hours or 7 days, per hour**, not just the last hour — bundles are usually collected after recovery, and an hour-only rule is blind to the incident it exists to surface. Open the YAML files directly to see the exact thresholds and tweak them.
