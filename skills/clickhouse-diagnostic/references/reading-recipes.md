# Reading recipes — querying the bundle's JSONL

Pick the tool in this order:

1. **`clickhouse local`** (ships with any `clickhouse` binary; `clickhouse local -q "..."`). Full SQL over `file('x.jsonl', JSONEachRow)`, exact `UInt64`, `formatReadableSize`, `quantile`. Best choice.
2. **Python 3 stdlib** (`json`, `collections`). Always available; safe with quoted 64-bit integers. `scripts/inspect_bundle.py` uses only this.
3. **`jq`** — fine for filtering/grouping strings and small ints; **never** for arithmetic on quoted `UInt64` (`tonumber` rounds above 2^53).

Conventions below: `B=<extracted bundle dir>`; files are matched with a glob because of the timestamp suffix. `clickhouse local` accepts globs directly: `file('$B/system.parts_*.jsonl', JSONEachRow)`. Quoted integers are auto-parsed when you give a schema or cast: `toUInt64(bytes_on_disk)`. Where a column is a human string (`system.disks.free_space`), use `free_pct` instead.

Discipline (from the analysis rules): select only the columns you will comment on; `LIMIT` every result (3–10 rows for raw, ≤ 30 aggregated); truncate query text to 150–300 chars; never paste whole tables into the summary.

## 0. Inventory and coverage

```bash
tar -tzf clickhouse_backup_*.tar.gz | head -50          # what is inside, before extracting
mkdir -p /tmp/chdiag && tar -xzf clickhouse_backup_*.tar.gz -C /tmp/chdiag
B=$(ls -d /tmp/chdiag/clickhouse_backup_*)
ls -la "$B"; wc -l "$B"/*.jsonl                          # 0-line files are meaningful (see bundle-layout §1)
cat "$B"/system.version_*.jsonl
head -c 400 "$B"/system.part_log_3_days_*.jsonl          # confirm column names on this bundle
grep -l '^### support-diagnostic: TRUNCATED' "$B"/logs/* 2>/dev/null
```

Collection window actually covered (query_log is the densest signal):
```sql
SELECT min(time), max(time), count() FROM file('$B/system.query_log_details_7_days_*.jsonl', JSONEachRow)
```
Python equivalent: `python3 skills/clickhouse-diagnostic/scripts/inspect_bundle.py "$B"`.

## 1. Alerts

From `dashboard.html` (includes matched rows):
```bash
python3 - "$B/dashboard.html" <<'EOF'
import json,re,sys
html=open(sys.argv[1],encoding='utf-8').read()
m=re.search(r'const DATA = (\{.*?\});\s*\n', html, re.S)
data=json.loads(m.group(1))
for a in data['alerts']:
    print(a['name'], a['severity'], 'fired' if a.get('rows') else ('error' if a.get('error') else 'clean'), len(a.get('rows') or []))
EOF
```
From `alerts_summary.json`: `jq -r '.rules[] | "\(.state)\t\(.severity)\t\(.name)\t\(.instance_count)"' "$B/alerts_summary.json"`.
Re-evaluating rules yourself from JSONL: thresholds in `health-checks.md`.

## 2. Parts and merges

Parts per partition (alert threshold 300, server throws at `parts_to_throw_insert`, OSS default 3000 / Cloud 10000 on new versions — check `system.tables.engine_full` for overrides):
```sql
SELECT database, table, partition_id, count() AS parts,
       formatReadableSize(sum(toUInt64(bytes_on_disk))) AS size, round(avg(toUInt64(rows))) AS avg_rows
FROM file('$B/system.parts_*.jsonl', JSONEachRow)
WHERE active = 1 AND database NOT IN ('system','INFORMATION_SCHEMA','information_schema')
GROUP BY 1,2,3 ORDER BY parts DESC LIMIT 10
```
Unmerged level-0 parts (inserts outrunning merges):
```sql
SELECT database, table, partition_id, countIf(level = 0) AS l0_parts, count() AS parts
FROM file('$B/system.parts_*.jsonl', JSONEachRow) WHERE active = 1
GROUP BY 1,2,3 HAVING l0_parts > 50 ORDER BY l0_parts DESC LIMIT 10
```
Tiny parts (batch size problem): `avg(toUInt64(rows)) < 10000` with `count() > 100` per table.
Wide vs compact: `GROUP BY part_type`. Oversized parts: `toUInt64(bytes_on_disk) > 150*1024*1024*1024`.

Merge activity and failures from `part_log` (bucketed sums — divide by `count`):
```sql
SELECT event_type, merge_reason, sum(toUInt64(count)) AS events,
       round(sum(toUInt64(duration_ms)) / sum(toUInt64(count))) AS avg_ms,
       formatReadableSize(sum(toUInt64(size_in_bytes))) AS bytes
FROM file('$B/system.part_log_3_days_*.jsonl', JSONEachRow)
GROUP BY 1,2 ORDER BY events DESC
```
Failed part operations: `WHERE error != 0` → `error, exception (sample), distinct_exceptions, sum(count)` grouped by `table_name, event_type`.
Insert rate proxy: `event_type = 'NewPart'` events per `time` bucket per `table_name` — many NewPart events with a small `size_in_bytes/count` = many small inserts.
TTL work: `merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')` or `event_type = 'RemovePart'`.

## 3. Replication and Keeper

```sql
SELECT database, table, is_readonly, is_session_expired, absolute_delay, queue_size, inserts_in_queue,
       merges_in_queue, parts_to_check, active_replicas, total_replicas,
       zookeeper_exception, last_queue_update_exception
FROM file('$B/system.replicas_*.jsonl', JSONEachRow)
ORDER BY is_readonly DESC, absolute_delay DESC LIMIT 10
```
Queue shape (any single `type` > 60 entries is a replica falling behind):
```sql
SELECT type, count(), countIf(last_exception != '') AS with_error, max(num_tries) AS max_tries, min(create_time) AS oldest
FROM file('$B/system.replication_queue_*.jsonl', JSONEachRow) GROUP BY type ORDER BY 2 DESC
```
A non-empty `zookeeper_exception` names the Keeper-side failure behind a read-only replica, and `last_queue_update_exception` the local queue-update failure; a high `max_tries` on a type that is still queued means the entry is retrying and failing rather than waiting its turn. Both sets are hashed in gov (empty stays empty, so "is it erroring at all?" survives).

Keeper health test (HC-3.8) — the two counters per hour against the 7-day median, with a verdict per hour:
```sql
WITH (SELECT quantileExactHigh(0.5)(toUInt64(zk_transactions)) FROM file('$B/system.metric_log_7_days_*.jsonl', JSONEachRow)) AS med
SELECT time, toUInt64(zk_hw_exceptions) AS hw, toUInt64(zk_transactions) AS tx, round(100 * tx / greatest(med, 1)) AS pct_of_median,
       multiIf(hw > 1000 AND tx < 0.5 * med, 'UNAVAILABLE', hw > 1000, 'blip', tx < 0.1 * med, 'idle/disconnected', 'ok') AS verdict
FROM file('$B/system.metric_log_7_days_*.jsonl', JSONEachRow)
WHERE verdict != 'ok' ORDER BY time
```
The same over the richer file (column names carry the aggregate): replace `zk_hw_exceptions` with `"sum(ProfileEvent_ZooKeeperHardwareExceptions)"` and `zk_transactions` with `"sum(ProfileEvent_ZooKeeperTransactions)"` in `system.metric_log_coordination_3_days_*.jsonl`; add `"max(CurrentMetric_ZooKeeperSession)"` (0 = no session that hour).

Session markers by hour (which minute, which Keeper host the server moved to):
```sql
SELECT toStartOfHour(event_time) AS h,
       countIf(message LIKE '%Session expired%') AS expired, countIf(message LIKE '%Finalizing session%') AS finalized,
       countIf(message LIKE '%Connected to ZooKeeper%') AS connected, countIf(message LIKE '%Trying to establish a new connection%') AS reconnecting,
       anyIf(leftUTF8(message, 160), message LIKE '%Connected to ZooKeeper%') AS example
FROM file('$B/system.text_log_2*.jsonl', JSONEachRow) GROUP BY h HAVING expired + finalized + connected + reconnecting > 0 ORDER BY h
```
(`system.text_log_keeper_1_day_*.jsonl` has the same counts precomputed for the whole day.) Mean Keeper latency per hour — the "saturated first" signal — from the coordination file: `"sum(ProfileEvent_ZooKeeperWaitMicroseconds)" / "sum(ProfileEvent_ZooKeeperTransactions)"`; failed operations by error from `system.zookeeper_log_errors_1_day_*.jsonl` (`op_num, error, failed_requests, sessions_affected`). Then 999/319/571 per hour from `query_log_details` (§6) and `MergeParts` with `error = 999` per hour from `part_log` (§2) — the hours must line up. Cumulative 999/242 in `system.errors` only says "since restart"; `system.error_log_7_days` (≥ 24.8, when present) gives them per hour, background threads included.

## 3a. Keeper and object-storage files (added for Keeper incidents)

Failed Keeper operations by hour and error (only present when `zookeeper_log` is enabled; the file holds failed responses only):
```sql
SELECT time, error, sum(toUInt64(failed_requests)) AS failed, max(toUInt64(sessions_affected)) AS sessions, groupArray(op_num) AS ops
FROM file('$B/system.zookeeper_log_errors_1_day_*.jsonl', JSONEachRow)
WHERE error IN ('ZSESSIONEXPIRED', 'ZCONNECTIONLOSS', 'ZOPERATIONTIMEOUT') GROUP BY time, error ORDER BY time
```
Every error code per hour, background threads included (`error_log`, ≥ 24.8):
```sql
SELECT time, error, sum(toUInt64(errors)) AS n FROM file('$B/system.error_log_7_days_*.jsonl', JSONEachRow)
WHERE code IN (999, 242, 252, 107, 319, 571) GROUP BY time, error ORDER BY time, n DESC
```
DDL that never finished, and replayed DDL (same statement under many entries):
```sql
SELECT status, count(), min(query_create_time) AS oldest, groupUniqArray(5)(host) AS hosts
FROM file('$B/system.distributed_ddl_queue_*.jsonl', JSONEachRow) WHERE status != 'Finished' GROUP BY status;
SELECT leftUTF8(query, 120) AS q, uniq(entry) AS entries, countIf(toInt32(exception_code) = 57) AS uuid_collisions
FROM file('$B/system.distributed_ddl_queue_*.jsonl', JSONEachRow) GROUP BY q HAVING entries > 1 ORDER BY entries DESC LIMIT 10
```
Object storage: is user data on it, and did uploads fail / deletes spike before the `FILE_DOESNT_EXIST` hour?
```sql
SELECT policy_name, disks FROM file('$B/system.storage_policies_*.jsonl', JSONEachRow);
SELECT name, type FROM file('$B/system.disks_*.jsonl', JSONEachRow);
SELECT time, event_type, sum(toUInt64(count)) AS ops, sumIf(toUInt64(count), toUInt8(failed) = 1) AS failed, anyIf(example_error, toUInt8(failed) = 1) AS err
FROM file('$B/system.blob_storage_log_7_days_*.jsonl', JSONEachRow) GROUP BY time, event_type ORDER BY time
```
Per-replica snapshot skew (cloud mode; one row per `hostname`) — the Keeper-held metadata cache and current sessions:
```sql
SELECT hostname, anyIf(value, metric = 'MetadataFromKeeperCacheObjects') AS cache_objects, anyIf(value, metric = 'ZooKeeperSession') AS zk_sessions,
       anyIf(value, metric = 'ReadonlyReplica') AS readonly_tables
FROM file('$B/system.metrics_*.jsonl', JSONEachRow) GROUP BY hostname ORDER BY cache_objects
```
Server uptime and the error rate it implies: `SELECT value FROM file('$B/system.asynchronous_metrics_*.jsonl', JSONEachRow) WHERE metric = 'Uptime'` → divide `system.errors.value` and `system.events.value` by it. Fetches still in flight: `system.replicated_fetches_*.jsonl` sorted by `elapsed`. Warning/Error volume per hour and component: `system.text_log_histogram_1_day_*.jsonl` (`time, level, logger_class, count, example`).

## 4. Disk and storage

```sql
SELECT name, type, free_pct, free_space, total_space FROM file('$B/system.disks_*.jsonl', JSONEachRow)
```
(`free_space`/`total_space` are strings; `free_pct` is numeric; cloud has one row per replica.) Cross-check `host_info.json → disks[]` where `mount_point` covers the ClickHouse `path` in `configuration/`. Size by database/engine from `system.parts` (`active = 1`, `sum(bytes_on_disk)`), compression ratio = `data_uncompressed_bytes / data_compressed_bytes`.

## 5. Memory and CPU

Server-side tracked memory and background pools per hour (the time series the dashboard does not draw):
```sql
SELECT time, formatReadableSize(avg_memory_tracking_bytes) AS mem, round(avg_merge_pool_tasks,1) AS merge_pool,
       max_merge_pool_tasks, round(avg_fetch_pool_tasks,1) AS fetch_pool, zk_transactions, zk_hw_exceptions
FROM file('$B/system.metric_log_7_days_*.jsonl', JSONEachRow) ORDER BY time
```
Compare `avg_memory_tracking_bytes` peaks with `host_info.memory.total_bytes` and `clickhouse_relevant_tunables.cgroup_memory_limit_bytes`; `max_merge_pool_tasks` against `background_pool_size` (default 16) — a pool pinned at its size for hours is saturated.

Host view: `host_info.json` → `cpu.logical_cpus`, `cpu.load_avg_1_5_15` (load ≫ CPUs = saturation at collection time), `memory.available_bytes` (< 2 GiB triggers ClickHouse's own startup warning), `swap_total_bytes - swap_free_bytes` (swap in use), `top_processes_by_rss` (is ClickHouse the only big process?).

Per-query memory (largest allocations by pattern). Use the canonical de-duplication CTE — it keeps one row per query group regardless of how many tables the query touched:
```sql
WITH dedup AS (
  SELECT * FROM file('$B/system.query_log_details_7_days_*.jsonl', JSONEachRow)
  WHERE type != 'QueryStart'
  LIMIT 1 BY time, query_kind, type, user, interface, normalized_query_hash, exception_code)
SELECT normalized_query_hash, any(user) AS user, sum(toUInt64(count)) AS runs,
       formatReadableSize(max(toUInt64(memory_usage)) / argMax(toUInt64(`count`),toUInt64(memory_usage))) AS mem_per_query_est,
       leftUTF8(any(query), 150) AS sample
FROM dedup WHERE type = 'QueryFinish'
GROUP BY 1 ORDER BY max(toUInt64(memory_usage)) DESC LIMIT 10
```
`memory_usage` in this file is a **sum over the hour bucket**, so divide by `count` for a per-query estimate. Reuse the same `dedup` CTE for every aggregate in §6; avoid the alias `sample` (it is a keyword) — use `q`/`msg`.

## 6. Query workload

Slowest patterns (per-execution average, filtered to one `tables` value):
```sql
SELECT normalized_query_hash, any(query_kind) AS kind, any(user) AS user,
       sum(toUInt64(count)) AS runs,
       round(sum(toUInt64(query_duration_ms)) / sum(toUInt64(count))) AS avg_ms,
       formatReadableSize(sum(toUInt64(read_bytes)) / sum(toUInt64(count))) AS avg_read,
       leftUTF8(any(query),150) AS sample
FROM file('$B/system.query_log_details_7_days_*.jsonl', JSONEachRow)
WHERE type = 'QueryFinish' AND tables != '' AND query_kind = 'Select'
GROUP BY 1 ORDER BY avg_ms DESC LIMIT 10
```
Failures by code: `WHERE exception_code != 0` → `exception_code, sum(count), any(exception), max(distinct_exceptions)`; join codes to names via `error-codes.md`. Queries per hour by kind: `GROUP BY time, query_kind`. Per user: `GROUP BY user`. Distinguish `type`: `ExceptionBeforeStart` (syntax/auth/limits) vs `ExceptionWhileProcessing` (runtime: memory, timeouts).

## 7. Mutations, TTL, dictionaries, detached parts

```sql
SELECT database, table, mutation_id, leftUTF8(command,120) AS command, create_time, parts_to_do
FROM file('$B/system.mutations_*.jsonl', JSONEachRow) ORDER BY create_time LIMIT 20
```
Age = collection time − `create_time` (the collection time is the folder timestamp). Dictionaries: `status != 'LOADED'`, `last_exception != ''`, `bytes_allocated` largest. Detached: `GROUP BY database, table, reason` with `count()` and `sum(bytes_on_disk)`.

## 8. Errors and logs

Cumulative error counters (since restart — compare against `uptime` from the dashboard or `os.uptime_seconds`):
```sql
SELECT code, name, toUInt64(value) AS total, last_error_time, leftUTF8(last_error_message,200) AS msg
FROM file('$B/system.errors_*.jsonl', JSONEachRow) ORDER BY total DESC LIMIT 15
```
text_log slice: `GROUP BY level, logger_name` counts, then read the newest 20 `Error`/`Fatal` rows. Check `min(event_time)` — 2000 rows may span only minutes.

Log files (grep, never read whole):
```bash
grep -c '' "$B"/logs/clickhouse-server.err.log
grep -oE '<(Error|Fatal|Warning)> [A-Za-z0-9_.:()]+' "$B"/logs/clickhouse-server.err.log | sort | uniq -c | sort -rn | head -20
grep -oE 'Code: [0-9]+' "$B"/logs/clickhouse-server.err.log | sort | uniq -c | sort -rn | head -20
grep -nE 'Starting ClickHouse|Ready for connections|Received termination signal|Shutting down|<Fatal>' "$B"/logs/clickhouse-server.log | head -20
grep -nE 'Available memory|transparent huge|overcommit|TaskStats|Linux' "$B"/logs/clickhouse-server.log | head   # startup warnings
```
A repeating identical line hundreds of times is a loop (retrying merge, stuck fetch) — count it, don't read it.

## 9. Schema facts from `system.tables`

```sql
SELECT database, name, engine, partition_key, sorting_key, storage_policy
FROM file('$B/system.tables_*.jsonl', JSONEachRow)
WHERE database NOT IN ('system','INFORMATION_SCHEMA','information_schema') AND engine LIKE '%MergeTree%'
ORDER BY database, name LIMIT 50
```
Join with §2 to explain part counts: high-cardinality `partition_key` (per-hour, per-tenant) + many partitions = expected fan-out; empty `sorting_key` (`tuple()`) = no primary index. `engine_full` carries per-table `SETTINGS` overrides (`parts_to_throw_insert`, `ttl_only_drop_parts`, `min_age_to_force_merge_seconds`…). MV chains: `engine = 'MaterializedView'` + `as_select`; count MVs per source table from `dependencies_table`.

## 9a. Changed settings

```sql
SELECT 'query' AS scope, name, value, `default` FROM file('$B/system.settings_*.jsonl', JSONEachRow) WHERE changed = 1
UNION ALL
SELECT 'server', name, value, `default` FROM file('$B/system.server_settings_*.jsonl', JSONEachRow) WHERE changed = 1
ORDER BY scope, name
```
(`default` is absent on < 23.4 roots — drop the column there.) Cross-check the pool/memory rows against `metric_log` and `host_info`; `REMOVED` values are gov redactions of identifying server settings, not errors.

## 10. Query analysis files (`A=$B/query_analysis`)

Identify the shape first — everything else depends on it:
```sql
SELECT query_id, query_kind, is_initial_query, user, initial_user, hostname, tables, databases, read_rows,
       formatReadableSize(toUInt64(read_bytes)) rd, query_duration_ms, memory_usage_human, peak_threads_usage, exception_code
FROM file('$A/query_details_*.jsonl', JSONEachRow)
```
`tables` like `['_table_function.s3Cluster']` → no MergeTree parts (skip `text_log_parts`/`tables_for_query`); `is_initial_query = 0` → worker sub-query, find the parent: `grep -o 'initial_query_id: [^)]*' $A/text_log_full_*.jsonl | head -1`.

Time budget of the focus execution (microsecond counters as a share of wall time). Wall time is `query_duration_ms`; `RealTimeMicroseconds` is **summed across threads** and overstates it:
```sql
WITH (SELECT toUInt64(query_duration_ms) * 1000 FROM file('$A/query_details_*.jsonl', JSONEachRow)) AS wall
SELECT metric, round(toUInt64(value)/1e6, 1) AS seconds, round(100 * toUInt64(value) / wall, 1) AS pct_of_wall
FROM file('$A/profile_events_*.jsonl', JSONEachRow)
WHERE metric IN ('RealTimeMicroseconds','OSCPUVirtualTimeMicroseconds','OSCPUWaitMicroseconds','UserTimeMicroseconds','SystemTimeMicroseconds',
  'NetworkSendElapsedMicroseconds','NetworkReceiveElapsedMicroseconds','ParquetFetchWaitTimeMicroseconds','ReadBufferFromS3Microseconds',
  'S3ReadMicroseconds','DiskReadElapsedMicroseconds','DiskWriteElapsedMicroseconds','ZooKeeperWaitMicroseconds','ThreadPoolReaderPageCacheMissElapsedMicroseconds')
ORDER BY toUInt64(value) DESC
```
Read it as: CPU used (`OSCPUVirtualTime`) vs CPU wait (`OSCPUWait` ≈ CPU used = throttled); network send share (rows shipped to initiator/client); storage waits; the rest is queueing/lock time. Sizes to pair with it: `SelectedRows/Bytes`, `SelectedParts/Marks`, `NetworkSendBytes`, `MemoryAllocatedWithoutCheckBytes`.

Runs or shards?
```sql
SELECT ts, leftUTF8(hostname, 30) host, read_rows, formatReadableSize(toUInt64(read_bytes)) rd, query_duration_ms,
       round(toUInt64(read_rows) / greatest(toUInt64(query_duration_ms), 1) * 1000) rows_per_s, memory_usage_human, exception_code
FROM file('$A/executions_timeline_*.jsonl', JSONEachRow) ORDER BY ts
```
Same minute, different hosts, `rows_per_s` similar while `read_rows` differs → shards (data skew across replicas/files). Different times, same host → runs; compare them with `profile_events_compare` (`ORDER BY abs(toInt64(delta)) DESC LIMIT 20`).

Where the time went inside one execution (`text_log_full`):
```sql
SELECT prev, ts, dateDiff('second', prev, ts) gap_s, leftUTF8(message, 120) m
FROM (SELECT ts, lagInFrame(ts) OVER (ORDER BY ts) prev, message FROM file('$A/text_log_full_*.jsonl', JSONEachRow))
WHERE gap_s > 10 ORDER BY gap_s DESC LIMIT 5
```
Also `grep -o 'Reading object .*size: [0-9]* bytes' $A/text_log_full_*.jsonl` for object-storage sources (file sizes reveal skew), and `text_log_parts` for MergeTree sources ("Selected N/M parts by partition key, K/L marks" — N = M means no part pruning).

Failures: `failed_over_time` (per minute by `error_type`), `failed_queries` (per table × code × user with `sample_exception`). Empty = no failures in the window.

## 11. Gov-mode joins

Hashes are stable per salt: `system.parts.table = system.replicas.table = system.mutations.table` for the same real table. Aggregate and rank by hash; label findings with the first 8 hex chars. If the user has `*_gov_name_mapping.csv` locally, join `table_hash` → `table` with `clickhouse local` or Python **on their machine**; do not paste the CSV anywhere.
