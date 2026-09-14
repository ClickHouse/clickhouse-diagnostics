# Running `clickhouse-diagnostic` — cheat-sheet and re-collection recipes

The full reference is the repository `README.md` (sections *Usage*, *Collection window*, *Modes and Query Layout*, *Gov mode*, *Host facts and server logs*, *Alerts*, *Query analysis mode*). This page is the subset an assistant needs to tell a user how to (re-)collect the right data.

## 1. Build or obtain the binary

```bash
make build                                  # → ./bin/clickhouse-diagnostic (Go ≥ 1.23)
make release                                # linux-amd64 / darwin-amd64 / darwin-arm64 / windows-amd64 in ./bin/
```
Ship the binary **together with** `queries.cloud/ queries.onprem/ queries.gov/ alerts/ queries.query_analysis/`, and run it from the directory that contains them — the query folders are resolved relative to the **current working directory**, not the binary. Otherwise: `Error: Queries folder './queries.<mode>' does not exist`.

## 2. Grants (read-only user)

```sql
CREATE USER sys_read_only IDENTIFIED WITH sha256_password BY '<password>';
GRANT SHOW DATABASES, SHOW TABLES ON *.* TO sys_read_only;
GRANT SELECT ON system.* TO sys_read_only;
-- cloud mode only (clusterAllReplicas fan-out):
GRANT REMOTE ON *.* TO sys_read_only;
GRANT CREATE TEMPORARY TABLE ON *.* TO sys_read_only;
```
A missing `SHOW` grant does **not** error — it silently shrinks `system.tables`/`columns`/`databases` to what the user can see. A bundle whose `system.tables` contains only `system.*` is the tell. Missing `REMOTE`/`CREATE TEMPORARY TABLE` in cloud mode fails loudly with 497.

Config, host facts and log collection read the **local filesystem** (`/etc/clickhouse-server`, `/var/log/clickhouse-server`, `/proc`) — they need OS read permission, not grants, and only make sense when the tool runs **on** the server.

## 2a. Passing the password from an agent or a script (read this before running)

The CLI takes the password from `-password <value>` **or** from an interactive prompt (`Enter Password:`, read from the TTY). There is no environment-variable option. Two things go wrong in non-interactive shells:

- **The prompt cannot be answered.** An agent's shell has no TTY, so the prompt reads an empty string and the run fails with `401 … Code: 194 … Authentication failed (REQUIRED_PASSWORD)`. If you see `Enter Password:` in the output, the flag never carried a value.
- **Inline environment assignment does not work.** `CH_PASS='x' ./clickhouse-diagnostic … -password "$CH_PASS"` expands `$CH_PASS` *before* the assignment takes effect, so the tool receives `-password ""`. This is a shell rule, not a tool bug.

Do this instead:

```bash
# 1. export first, run second (two statements)
export CH_PASS='<password>'
./clickhouse-diagnostic -mode cloud -host <svc> -port 8443 -protocol https -user default -password "$CH_PASS" -skip-config

# 2. or read it from a file outside the repository, mode 0600, deleted afterwards
umask 077; printf '%s' '<password>' > ~/.chdiag_pass
./clickhouse-diagnostic … -password "$(cat ~/.chdiag_pass)"
rm -f ~/.chdiag_pass

# 3. or let a human run it interactively (the prompt hides the input)
./clickhouse-diagnostic -mode cloud -host <svc> -port 8443 -protocol https -user default -skip-config
```

Rules for the assistant: never write the password into a file inside the repository (even a gitignored one — it can still be shipped or pasted); never echo it back in the summary; the value stays in the shell history of this session, so tell the user to rotate it when the collection is done; when the user gives the password in chat, use pattern 1 and say that you did.

## 3. Modes

| `-mode` | Reads | Notes |
|---|---|---|
| `onprem` (default) | `system.*` of the connected node | host facts + logs **on** by default (`-host-info/-logs auto`) |
| `cloud` | `clusterAllReplicas(default, system.*)` for per-replica tables | host facts/logs off by default (they would describe your laptop); use `-skip-config` |
| `gov` | `system.*` with database/table/user/host names hashed (`SHA256(name || salt)`) | requires `-salt` (8–64 alphanumerics, keep it private); no dashboard, no query text, no config/host/logs/query-analysis; writes `alerts_summary.json` and a **local-only** mapping CSV |

**Self-hosted SharedMergeTree clusters** (`cloud_mode = 1` in `system.settings`, `Shared*MergeTree` engines, an `s3_with_keeper` disk): every replica keeps its own `system.*` tables, so `-mode onprem` collects **one node of N** (its parts, errors, part_log, query_log, text_log). The tool prints a warning when it sees `cloud_mode = 1`. Ask for `-mode cloud` for the cluster-wide view (fans out over the `default` cluster; needs `REMOTE` + `CREATE TEMPORARY TABLE`) **plus** one `onprem` run on a node for host facts, configuration and log files.

## 4. Common invocations

```bash
# on-prem node, everything, default 7-day windows
./clickhouse-diagnostic -mode onprem -host localhost -port 8123 -user sys_read_only

# ClickHouse Cloud
./clickhouse-diagnostic -mode cloud -host <svc>.<region>.aws.clickhouse.cloud -port 8443 -protocol https \
  -user sys_read_only -skip-config

# gov / hashed identifiers
./clickhouse-diagnostic -mode gov -host gov-ch-01 -salt <YourPrivateSalt>

# non-interactive (CI / agent): export the variables in a previous statement — see §2a
export CH_HOST=… CH_USER=… CH_PASS=…
./clickhouse-diagnostic -mode onprem -host "$CH_HOST" -user "$CH_USER" -password "$CH_PASS" -skip-config

# security review: print every query it would run + EXPLAIN ESTIMATE, collect nothing
./clickhouse-diagnostic -mode onprem -host ch-01 -dry-run
```

Collectors that read Keeper (`system.distributed_ddl_queue`, `system.zookeeper_connection`) or a very large log table (`system.zookeeper_log`) are bounded by `LIMIT` and by the timeout below; on an unhealthy Keeper they may be the ones that time out (code 159), which is itself evidence.

Useful flags: `-query-timeout` (default 240 s — the server enforces it as `max_execution_time`, so a collector query that overruns shows up as a clean `Code: 159` in the customer's `query_log`; `0` disables), `-output-dir` (default `./clickhouse_results`), `-output-format jsonl|native|tsv` (**leave at `jsonl`** — this skill and `inspect_bundle.py` can only read `.jsonl`, and a `native`/`tsv` bundle is refused; see bundle-layout §1), `-skip-alerts`, `-skip-dashboard`, `-skip-archive`, `-alerts-dir`, `-config-dir`, `-logs-dir`, `-logs-max-mb` (default 50), `-logs-include-archives`, `-host-info on|off|auto`, `-logs on|off|auto`.

## 5. Time windows

Collection windows are per query: 7 days for `query_log`, `metric_log` (fixed columns), `asynchronous_insert_log`, `blob_storage_log`, `error_log`, `distributed_ddl_queue`; **3 days** for `part_log` and `metric_log_coordination` (the two wide or high-volume ones); **1 day** for `system.text_log` (capped at 2000 rows), the text_log histogram and Keeper markers, and `zookeeper_log` errors. `-from`/`-to` (RFC3339 or `YYYY-MM-DD`, UTC) override **every** window at once; alert rules keep their own windows by design.

```bash
# exactly the incident window (cheapest, most focused)
./clickhouse-diagnostic -mode onprem -host ch-01 -from 2026-08-14T09:00:00Z -to 2026-08-14T13:00:00Z
# widen to 30 days
./clickhouse-diagnostic -mode onprem -host ch-01 -from 2026-07-21
```

## 6. Deeper slices

```bash
# a bounded slice of system.text_log (needs both bounds; not in gov)
./clickhouse-diagnostic -mode onprem -host ch-01 -collect-text-log \
  -from 2026-08-20T14:00:00Z -to 2026-08-20T15:00:00Z -text-log-level Warning -text-log-limit 200000

# query analysis for one query_id (window auto-centred on its event_time)
./clickhouse-diagnostic -mode onprem -host ch-01 --query-id 1bc3abaf-968f-4d4f-be3d-f77251b1ff0b

# query analysis for a pattern (normalized_query_hash from the bundle's query_log_details), last 7 days
./clickhouse-diagnostic -mode onprem -host ch-01 --normalized-query-hash 7769688026807387533 -from 2026-05-23 -to 2026-05-30
```
Query analysis writes 12 files to `<backup>/query_analysis/` and adds a dashboard section; it is rejected in gov mode.

## 7. Output

```
clickhouse_results/clickhouse_backup_YYYYMMDD_HHMMSS/   # the folder that is archived
clickhouse_results/clickhouse_backup_YYYYMMDD_HHMMSS/execution_log.txt   # what ran, what failed, what it cost
clickhouse_backup_YYYYMMDD_HHMMSS.tar.gz                # in the CWD — send this
clickhouse_results/clickhouse_backup_<ts>_gov_name_mapping.csv   # gov only — never send
```
Before sharing: open `configuration/` and confirm nothing sensitive remains (sanitisation strips credentials, not hostnames/topology); in gov mode confirm the salt and CSV are not inside the archive.

## 8. Re-collection recipes keyed to findings

| Finding | Re-run with |
|---|---|
| Incident outside the collected window | `-from <start> -to <end>` around the incident (hours, not days) |
| Need the slow/failed pattern explained | `--normalized-query-hash <hash>` (from `query_log_details`) or `--query-id <uuid>` (from the app/log) |
| `text_log` slice too short (2000 rows = minutes) | `-collect-text-log -from … -to … -text-log-level Warning` |
| Logs truncated (`TRUNCATED` header) or rotated files needed | `-logs-max-mb 500 -logs-include-archives` |
| `host_info.json` missing/degraded (tool ran remotely) | run the tool **on** the server, or `-host-info on` there |
| Config missing | `-config-dir /etc/clickhouse-server` (point at the directory holding `config.xml`, `config.d/`, `users.d/`) |
| Bundle shows only `system` tables | grant `SHOW DATABASES, SHOW TABLES ON *.*` and re-run |
| Cloud: some tables errored with 497 | grant `REMOTE` + `CREATE TEMPORARY TABLE`, re-run |
| Gov bundle lacks what the analysis needs | if policy allows, collect an `onprem` bundle and share only the summary; otherwise use the mapping CSV locally |
| Track a trend (parts growth, error rates) | collect a second bundle hours/days later and diff `system.parts` counts and `system.errors` values |
| Keeper incident on a multi-replica / SharedMergeTree cluster | `-mode cloud` for all replicas' `metric_log_coordination`, `zookeeper_connection`, `metrics` (`MetadataFromKeeperCacheObjects` per replica), `part_log`; plus the Keeper logs and `echo mntr \| nc <keeper> <port>` from every Keeper member (not collected by the tool) |
| `zookeeper_log_errors_1_day` / `blob_storage_log_7_days` missing | the tables are not enabled: `<zookeeper_log>` / `<blob_storage_log>` in the server config (both have a TTL knob); enable, wait for the next incident, or ask for `system.remote_data_paths` for one key |

## 9. What the bundle deliberately does not contain

Customer rows (never queried); `system.zookeeper` (the tree itself), `system.remote_data_paths` (one row per blob — too large), `system.filesystem_cache` (one row per segment), `system.trace_log`, `system.processors_profile_log` (except in query analysis), `system.merge_tree_settings`, `system.users/grants`, `system.backups`, `system.query_views_log`, `system.row_policies`, Keeper logs/`mntr` output, and `system.zookeeper_log` / `system.blob_storage_log` when the server does not have them enabled. When a finding needs one of these, list the exact `SELECT` the user should run (single isolating query, `LIMIT`ed) rather than asking for a dump.
