---
name: clickhouse-diagnostic
description: >-
  Analyse a ClickHouse support-diagnostic bundle (clickhouse_backup_*.tar.gz or
  its extracted folder: system.* JSONL dumps, dashboard.html, host_info.json,
  logs/, configuration/) and produce an evidence-backed health summary — parts
  and merges, replication and Keeper, disk, memory/CPU, queries, inserts,
  mutations/TTL, host tunables — with recommended actions and follow-up prompts.
  Also explains how to run clickhouse-diagnostic (grants, modes, windows,
  query analysis, gov mode) and what to re-collect. Use when the user mentions a
  diagnostic bundle, support bundle, clickhouse_backup, dashboard.html,
  "run the diagnostic", "what does this file in the bundle mean", or asks why
  their ClickHouse server is slow, OOMing, has too many parts, is read-only or
  is losing inserts and has (or can collect) a bundle. Works offline; network
  only for optional version research on GitHub.
user-invocable: true
---

# clickhouse-diagnostic — read a bundle, explain the server

The bundle is produced by `clickhouse-diagnostic` (this repository). It is a **read-only snapshot of one ClickHouse server or service**: ~20 `system.*` tables as JSONL, alert results, optionally host facts, config and server logs. Your job is to turn it into a short, evidence-backed summary the owner can act on, and to say clearly what the bundle cannot show.

## The one rule: evidence first, bundle untouched

Every finding cites `file → column → value`. Never modify the bundle. Never send bundle content to any service, and never paste identifiers into anything that leaves the machine without the privacy gate (step 7). Never call anything a "known issue" — say "resembles" / "consistent with" and let the owner or ClickHouse confirm. If the data does not cover the question, say so and give the exact re-collection command instead of guessing.

## When to use / not use

- **Use** for: a `clickhouse_backup_*.tar.gz` or extracted folder; questions about what a bundle file/column means; "how do I run the tool / which grants / which mode"; "what should I collect for this problem".
- **Don't use** for: live troubleshooting of a running server (no bundle) — offer to explain how to collect one; problems outside ClickHouse (application code, Kafka brokers, cloud IAM) beyond what the bundle shows.

## Read-before-you-act table (lazy loading — read only what the step needs)

| When | Read |
|---|---|
| Before opening any file in a bundle | `references/bundle-layout.md` (what is in the archive, columns, encodings) |
| Deciding what to compute from a given file, or what "healthy" looks like for it | `references/file-guide.md` — one entry per file: why we run it, question it answers, read-first list, healthy baseline, red flags, traps; plus scenario walkthroughs (replicated cluster under load, TOO_MANY_PARTS, 241, disk, upgrade regression, crash) |
| Writing any query/`jq`/Python over the JSONL | `references/reading-recipes.md` |
| Step 2 (health pass) | `references/health-checks.md` |
| An error code appears (`system.errors`, `exception_code`, `Code: NNN`) | `references/error-codes.md` |
| A finding matches a symptom (parts, memory, Keeper, mutations, …) | `references/known-patterns.md` — scan its quick-reference table, open only matching entries |
| The user asks about their version, a setting's default, a bug, or an upgrade (network) | `references/clickhouse-source.md` |
| The user has no bundle, the window is wrong, or data is missing | `references/running-the-tool.md` |
| Writing the summary | `references/summary-template.md` |
| Before any text or file leaves the machine | `references/privacy.md` |

## Workflow

### 0 — Intake and coverage (mandatory before any conclusion)
1. Locate the input: a `.tar.gz`, or a folder containing `system.version_*.jsonl`, or a parent `clickhouse_results/` with one `clickhouse_backup_*` inside. Ask if there are several.
2. Run the deterministic pre-pass — it extracts safely (to a temp dir), inventories the files and computes the checks that need no SQL:
   ```bash
   python3 <skill-dir>/scripts/inspect_bundle.py <bundle.tar.gz|dir>          # markdown
   python3 <skill-dir>/scripts/inspect_bundle.py <bundle> --json > /tmp/inspect.json
   ```
   `<skill-dir>` is the directory holding this SKILL.md. Reuse the printed extraction directory (`B=…`) for every later recipe.
3. State the **coverage** in ≤ 6 lines: ClickHouse version; mode (cloud/onprem/gov — detection rules in bundle-layout §3); collection timestamp; query_log window actually covered; text_log rows and span; files empty/absent and why (healthy-empty vs not collected); `TRUNCATED` log headers; whether `host_info.json` describes the server or another machine; signs the collector lacked grants (only `system` tables visible, 497 in `system.errors`).
4. If the user's incident is outside the covered window, say so now and propose the `-from/-to` re-run (running-the-tool §8). Continue with what the bundle *does* cover.

### 1 — Triage the headline
From the pre-pass and `dashboard.html` `DATA.alerts` / `alerts_summary.json`: fired alerts (never count "could not run" as findings), `crash_log` rows, read-only replicas, disks < 15 %, top `system.errors` codes relative to uptime (`asynchronous_metrics.Uptime`), `replication_queue.last_exception`, the **incident hours** — hours where `metric_log_coordination` shows Keeper hardware exceptions, `part_log` shows inserts without merges, or `query_log` exceptions spike (the pre-pass prints an hourly timeline). Write the 1–3 headline bullets; everything else is detail.

### 2 — Area health pass
Walk `health-checks.md` HC-1 → HC-11 in order — and run the **Keeper health test** (HC-3.8: hardware exceptions *and* transactions per hour against the 7-day median) whenever any 999/319/571, read-only replica or stalled-merge hour appears — opening the `file-guide.md` entry for each file you touch and using the recipes in `reading-recipes.md` (`clickhouse local` if available, else Python; `jq` only for non-numeric filtering). Record one status per area with its single most specific evidence line. Respect the two data traps: `query_log_details` rows are duplicated per table (`LEFT ARRAY JOIN tables`) — fix one `tables` value before summing; `system.errors.value` is cumulative since restart — compare with uptime, never treat as a rate. Read `system.parts` with `active = 1` only.

### 3 — Query analysis (only if `query_analysis/` exists)
Read `file-guide.md` → *query_analysis/* first. Order matters:
1. **Identify the shape** from `query_details`: `tables` (a MergeTree table, or a table function such as `_table_function.s3Cluster`/`s3`/`remote`), `is_initial_query`, `query_kind`, `user`/`initial_user`. If `is_initial_query = 0`, the hash is a **worker sub-query** of a distributed statement — the "executions" are shards of one run, and slow-vs-fast measures data skew, not a regression. Say so and analyse per-replica shares.
2. **Time budget** from `profile_events` of the focus execution: `query_duration_ms` as wall (not `RealTimeMicroseconds`, which is summed over threads) vs `OSCPUVirtualTimeMicroseconds` (CPU used), `OSCPUWaitMicroseconds` (CPU throttled), `NetworkSendElapsedMicroseconds` (shipping rows), `ParquetFetchWaitTimeMicroseconds`/`ReadBufferFromS3Microseconds`/`S3ReadMicroseconds` (object storage), `DiskReadElapsedMicroseconds`, `MemoryTracker*`. Name the dominant component before anything else.
3. **Runs vs shards**: `executions_timeline` + `hash_by_host` — same `ts` cluster on different hosts with rows proportional to duration = shards; spread over time on any host = runs.
4. `profile_events_compare` (largest `|delta|`) only when step 3 says "runs"; `text_log_parts` ("Selected N/N parts" = no pruning) and `tables_for_query` only when `tables` is a MergeTree table; `text_log_full` for the onset (`Reading object…`, `Selected…`, the longest gap between lines); `failed_*` for when errors started.
5. Empty files are expected for shapes they do not apply to (table functions → `text_log_parts`, `tables_for_query`; no failures → `failed_*`); say "not applicable", not "not collected".
Tie the result to P-50…P-56.

### 4 — Pattern match
For each warning/critical finding, scan the quick-reference table in `known-patterns.md` and open the matching entry. Use its *Verify* step with a second, independent source in the bundle before promoting the match to a recommendation. Use the language rules from `clickhouse-source.md` §4.

### 5 — Version research (optional, network, ask first)
Only if the user wants it or a finding smells version-specific (P-30, P-31, P-54): resolve `version` → tag, check release notes between the customer's patch and the latest patch of the minor, verify settings in source at the tag, search issues by exact error text. Follow the four-step citation protocol; never invent issue numbers.

### 6 — Write the summary
Use `summary-template.md`: coverage → headline → scorecard → findings (severity-ordered, each with evidence, threshold, action, confidence) → recommendations → **suggested follow-up prompts** → what to re-collect → caveats. Measurements, not attributions. Numbers with their thresholds. ≤ 7 findings.

### 7 — Privacy gate
Before any output is pasted into a ticket, shared, or written to a file outside the scratch dir: run the checklist in `privacy.md` (hostnames, IPs, db/table/column/user names, query literals, paths, config excerpts). Never ask for the gov salt; use a mapping CSV only locally. Do not write into the bundle directory.

## Running the tool (when the user has no bundle)

Point them to `references/running-the-tool.md`: grants (`SHOW DATABASES, SHOW TABLES ON *.*` + `SELECT ON system.*`; cloud adds `REMOTE` + `CREATE TEMPORARY TABLE`), `-mode onprem|cloud|gov`, run from the directory that contains `queries.<mode>/`, `-from/-to` for the incident window, `--normalized-query-hash`/`--query-id` for one query, `-dry-run` for a security review. Tell them what the run will and will not collect before they send anything to a third party. **Password:** the CLI has only `-password` and a TTY prompt — from an agent shell the prompt reads empty and the run fails with 401 / code 194. `export CH_PASS=…` in one statement and pass `-password "$CH_PASS"` in the next; never `VAR=x cmd "$VAR"` on one line, never write the password into the repository, and remind the user to rotate it afterwards (running-the-tool.md §2a).

## Hard negatives

- Do **not** sum `query_log_details` across rows without fixing one `tables` value (multiply-counting).
- Do **not** treat `system.errors.value` as a rate; pair it with uptime or a second bundle.
- Do **not** parse quoted 64-bit integers as JavaScript/`jq` numbers.
- Do **not** conclude "no errors" from `system.text_log` (24 h, 2000-row cap) — check its actual span.
- Do **not** trust `host_info.json` when the tool ran on a different machine than the server.
- Do **not** read an `onprem` bundle from a SharedMergeTree / `cloud_mode = 1` cluster as the cluster: it is one replica of N (parts, errors, part_log, query_log, text_log are per replica). Say which node, and propose `-mode cloud`.
- Do **not** conclude "Keeper was fine" from `query_log`: background merges, fetches and part commits never appear there. Read `metric_log_coordination_7_days` (`ZooKeeperHardwareExceptions` per hour) and `part_log` errors first.
- Do **not** treat an absent `zookeeper_log_1_day` / `blob_storage_log_7_days` as evidence of anything — both tables exist only when configured.
- Do **not** call anything a known issue, name a fix version you have not verified, or cite an issue you have not opened.
- Do **not** promote a measurement to an attribution in customer-facing text.
- Do **not** modify, re-pack, or upload the bundle; do not paste `configuration/`, `logs/`, `host_info.json` or DDL verbatim outside the machine.
- Do **not** rely on alert *names* for error codes — check the code (tool versions before September 2026 shipped a `too_many_simultaneous_queries` rule that filtered 252 = TOO_MANY_PARTS, and a `too_many_parts` message citing "code 497", which is ACCESS_DENIED; bundles from those versions carry the mislabelled alert text).

## Output style

Brief. Short paragraphs, one idea per line, no manual wrapping. Tables for the scorecard and evidence; prose for reasoning. Cite bundle files by glob (`system.parts_*.jsonl`). End with the follow-up prompts so the user can go one level deeper in a single message.
