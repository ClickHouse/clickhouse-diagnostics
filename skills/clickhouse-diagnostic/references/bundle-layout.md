# Bundle layout — what is in a `clickhouse_backup_*` archive and how to read each file

Read this before opening any file in a bundle. It describes the archive produced by `clickhouse-diagnostic` (this repository). For *what each file is for, what to read first and what healthy looks like*, read the matching entry in `file-guide.md` before the column table below. The collector evolves: **always confirm column names against the first line of the file you are reading** — the tables below describe the current SQL in `queries.<mode>/`, and an older bundle may carry a slightly different column set (e.g. an early `part_log` file also had `part_name` and an unaliased `any(exception)` column).

## 1. Tree

```
clickhouse_backup_YYYYMMDD_HHMMSS/            # single top-level entry of the .tar.gz
├── system.<table>[_7_days|_3_days|_1_day]_<ts>.jsonl  # one file per collection query (JSONEachRow); the suffix is the window — bundles from older tool builds name part_log `_7_days`
├── text_log_<ts>.jsonl                        # only with --collect-text-log (UTC stamp, may differ by 1 s)
├── query_analysis/<name>_<ts>.jsonl           # only with --query-id / --normalized-query-hash (12 files)
├── configuration/…                            # sanitised XML/YAML, source tree preserved (never in gov)
├── host_info.json                             # onprem by default (never in gov)
├── logs/*.log                                 # onprem by default (never in gov)
├── dashboard.html                             # unless -skip-dashboard or gov
├── execution_log.txt                          # every collector: outcome, wall time, bytes, rows; alerts; phases (read first)
└── alerts_summary.json                        # only when alerts ran but dashboard.html is absent
clickhouse_backup_<ts>_gov_name_mapping.csv    # NEXT TO the folder, never inside the archive (gov only)
```

- `<ts>` in the folder name is the run start in the **collector host's local time**; `text_log_<ts>` and `query_analysis/*_<ts>` are stamped in **UTC** at write time.
- Extension follows `-output-format`: `.jsonl` (default, `JSONEachRow`), `.native`, or `.tsv` (`TSVWithNamesAndTypes`: names on line 1, **types on line 2**).
- **Only `.jsonl` can be analysed.** Every recipe in this skill and every reader in `scripts/inspect_bundle.py` globs `*.jsonl`; on a `.native` or `.tsv` bundle they find nothing and would report "0 parts, no findings" — a false all-clear indistinguishable from a healthy server. `inspect_bundle.py` therefore refuses such a bundle outright (exit 2) instead of misreading it. Ask for a re-collection with the default `-output-format jsonl`; do **not** hand-wave the counts as zero.
- Empty files are normal and meaningful: a 0-byte `system.crash_log_*.jsonl` means no crash; 0-byte `system.merges_*.jsonl` means nothing was merging at collection time.

## 2. Value encoding rules (JSONL)

| Rule | Why it matters |
|---|---|
| 64-bit integers are **quoted strings** (`"normalized_query_hash":"3513886418292139323"`, `"count":"1"`, `"bytes_on_disk":"438"`) | The tool pins `output_format_json_quote_64bit_integers=1`. Parse with `int()`/`toUInt64()`; **never** with JavaScript numbers or `jq` arithmetic (silently rounds above 2^53). |
| 8/16/32-bit ints, floats and booleans are bare (`"error":0`, `"interface":2`, `"avg_merge_pool_tasks":0.0348`) | |
| Enums arrive as their **name** (`"event_type":"NewPart"`, `"type":"QueryFinish"`, `"level":"Warning"`) | Do not expect integer codes. |
| DateTime is `"YYYY-MM-DD HH:MM:SS"` in the **server's** time zone; `toString()`-wrapped columns are the same shape | Compare with the server's `timezone()` in mind when correlating with UTC log files. |
| Arrays are JSON arrays; `Map` columns (e.g. `ProfileEvents`) are JSON objects | |
| Text columns were cut with `leftUTF8(...)`, so every line is valid UTF-8 | Strict parsers (Python `json`) are safe. |

## 3. Detecting the mode

| Signal | cloud | onprem | gov |
|---|---|---|---|
| `dashboard.html` present | yes | yes | **no** (withheld) |
| `alerts_summary.json` | only if `-skip-dashboard` | only if `-skip-dashboard` | **yes** (its only purpose) |
| `system.crash_log`, `system.stack_trace` files | yes | yes | **absent** |
| `database`/`table`/`user`/`host_name` values | plain | plain | 64-hex-char SHA-256 hashes |
| `system.query_log_details_7_days` has `query`, `exception` | yes | yes | **no** |
| `system.text_log.message` | text | text | 64-hex hash (unreadable) |
| `hostname` column in `part_log`/`query_log` files | yes (fan-out over replicas via `clusterAllReplicas`) | only on servers ≥ 23.11 (single host) | ≥ 23.11 (hashed) |
| `host_info.json`, `logs/`, `configuration/` | off by default | on by default | never |

Also read `system.version_*.jsonl` (`{"version":"25.3.2.39"}`) first — every version-dependent statement in this skill hangs off it.

## 4. Collection queries (`queries.<mode>/`) — file by file

Purpose, first reads, healthy baseline, red flags and traps for every file: `file-guide.md`. This section is the column-level reference.

Window placeholders: `{from:7d}`/`{to:now}` = last 7 days unless `-from`/`-to` were given; `{from:1d}` = last 24 h. Snapshot files have no window: they describe the instant of collection.

### Point-in-time snapshots

| File | Columns (onprem/cloud) | Gov differences | Notes |
|---|---|---|---|
| `system.version` | `version` | same | |
| `system.clusters` | `cluster, shard_num, shard_weight, replica_num, host_name, host_address, port, is_local, user, default_database, errors_count, slowdowns_count, estimated_recovery_time` (+ `database_shard_name, database_replica_name, is_active, name` on ≥ 23.5) | hashes cluster, host_name, host_address, port, user, default_database (+ shard/replica/name) | `errors_count != 0` = a node has failed distributed queries. |
| `system.tables` | `database, name, uuid, engine, is_temporary, data_paths, metadata_path, metadata_modification_time, dependencies_*, create_table_query, engine_full, as_select, partition_key, sorting_key, primary_key, sampling_key, storage_policy, comment, has_own_data, loading_dependencies_*, loading_dependent_*` (+ `metadata_version` ≥ 24.2, `parameterized_view_parameters` ≥ 25.4) | **8 columns only**: hashed `database`, `name`, plus `uuid, engine, is_temporary, metadata_modification_time, storage_policy, has_own_data` (+ `metadata_version`). No DDL, no keys. | Full DDL and ORDER BY live here — the source for schema-design findings. |
| `system.columns` | `database, table, name, type, position, default_kind, default_expression` | hashes database/table/name; no `default_expression` | Large (one row per column). |
| `system.settings` | `name, value, changed, type` (+ `default` ≥ 23.4; cloud always) | same (values are **not** hashed — they are tuning knobs, not identifiers) | Session/profile settings as the collector's user sees them. `changed = 1` rows are the customised ones; compare `value` with `default`. |
| `system.server_settings` (≥ 23.4; cloud root) | `name, value, default, changed, type` | path- or URL-shaped values and `default_database`, `default_replica_name`, `interserver_http_host`, `default_profile`, `merge_workload`, `mutation_workload` are replaced by the literal `REMOVED` | `config.xml`-level settings (`max_server_memory_usage*`, `background_pool_size`, `max_concurrent_queries`, caches, `keep_free_space_bytes`…). Absent on servers < 23.4 (no root file in onprem/gov). |
| `system.parts` | 41 cols: `partition, name, part_type, active, marks, rows, bytes_on_disk, data_compressed_bytes, data_uncompressed_bytes, secondary_indices_*, marks_bytes, modification_time, remove_time, refcount, min_date, max_date, min_time, max_time, partition_id, min_block_number, max_block_number, level, data_version, primary_key_bytes_in_memory(_allocated), is_frozen, database, table, engine, disk_name, path, hash_of_*, delete_ttl_info_min/max, move_ttl_info.*` | hashes name/database/table/`partition`; drops disk_name, path, hash_* | Current collectors return **active parts only, largest first, capped at 50 000 rows** (`WHERE active = 1 ORDER BY bytes_on_disk DESC LIMIT 50000`); bundles from before September 2026 include inactive parts — always filter `active = 1` to be safe. If the file has exactly 50 000 rows the smallest parts were cut off. Single replica even in cloud (parts are shared). |
| `system.detached_parts` | `database, table, partition_id, disk, reason, min_block_number, max_block_number, level` (+ `bytes_on_disk, path` ≥ 22.11; + `modification_time` ≥ 23.11) | hashed db/table; no disk/path | `reason` explains why (`broken`, `unexpected`, `ignored`, `clone`, `covered-by-broken`…). |
| `system.disks` | `name, path, free_space, total_space, used_space, free_pct, type` (+ `unreserved_space` ≥ 22.10) | hashes name/path | Sizes are **human strings** (`"12.34 GiB"`); `free_pct` is numeric. Cloud: one row per replica. |
| `system.errors` | `name, code, value, last_error_time, last_error_message, last_error_trace` (trace cut at 500) | message hashed, trace empty | **Cumulative counters since server start**, top 100 by `value`. A rate needs two bundles or `uptime`. |
| `system.merges` | `database, table, elapsed, progress, num_parts, result_part_name, is_mutation, total_size_bytes_compressed, total_size_marks, bytes_read_uncompressed, rows_read, bytes_written_uncompressed, rows_written, memory_usage, thread_id, merge_type, merge_algorithm` | hashes db/table | Empty = nothing merging *at that instant*, not "merges are broken". |
| `system.mutations` | `database, table, mutation_id, command, create_time, parts_to_do_names, parts_to_do` (+ `is_killed` in cloud) | hashes db/table/command | **Includes finished mutations**: the server keeps up to `finished_mutations_to_keep` (default 100) per table, so a row count is not a pending count — filter `parts_to_do > 0` (HC-8.2 does). |
| `system.replicas` | `database, table, is_leader, can_become_leader, is_readonly, is_session_expired, future_parts, parts_to_check, queue_size, inserts_in_queue, merges_in_queue, part_mutations_in_queue, queue_oldest_time, log_max_index, log_pointer, absolute_delay, total_replicas, active_replicas, last_queue_update_exception, zookeeper_exception` ORDER BY `absolute_delay DESC` | hashes db/table and both exception strings (empty stays empty) | Empty file on a server with no `Replicated*` tables. `zookeeper_exception`/`last_queue_update_exception` are the reason behind `is_readonly = 1`; absent from bundles collected before this column set. |
| `system.replication_queue` | `create_time, database, table, type, replica_name, is_currently_executing, position, num_tries, postpone_reason, last_exception, merge_type` | hashes database/table/replica_name/reasons (empty stays empty); `num_tries` is a plain counter | `type` ∈ GET_PART, MERGE_PARTS, MUTATE_PART, ATTACH_PART, DROP_RANGE, ALTER_METADATA… `num_tries` separates a slow queue from a stuck one; `database` and `num_tries` are absent from bundles collected before this column set. |
| `system.processes` | `user, address, elapsed, read_rows, read_bytes, total_rows_approx, memory_usage, query_id, is_cancelled, is_all_data_sent` | hashes user/address | The collector's own query is usually in here. |
| `system.dictionaries` | `database, name, uuid, status, origin, type, bytes_allocated, query_count, hit_rate, found_rate, element_count, load_factor, source, lifetime_min, lifetime_max, loading_start_time, last_successful_update_time, loading_duration, last_exception, comment` | hashes db/name/origin; no source/last_exception/comment | `status` ∈ LOADED, FAILED, LOADING, NOT_LOADED, FAILED_AND_RELOADING. |
| `system.crash_log` | `SELECT *` (event_time, signal, thread_id, query_id, trace, trace_full, version, revision, build_id) | not collected | Any row = the server received a fatal signal. |
| `system.stack_trace` | `SELECT *` (one row per thread) | not collected | Snapshot of what every thread was doing at collection time. |
| `system.metrics` | `metric, value, description` (cloud: `hostname, metric, value`, one row per replica) | same | CurrentMetrics snapshot: `ZooKeeperSession` (0 = no Keeper session), `ZooKeeperWatch/Request`, `ReadonlyReplica`, `ReplicatedFetch/Send`, `S3Requests`, `FilesystemCacheSize`, `MetadataFromKeeperCacheObjects` (object storage with metadata in Keeper), `Background*PoolTask`, `DelayedInserts`, `MemoryTracking`. |
| `system.events` | `event, value, description` where `value > 0` (cloud: `hostname, event, value`) | same | **Cumulative since server start** like `system.errors`: `ZooKeeper{User,Hardware,Other}Exceptions`, `S3*RequestsErrors`, `ReadBufferFromS3RequestsErrors`, `ReplicatedPartFailedFetches`, `FailedQuery`, `RejectedInserts`, `DelayedInserts`. Divide by `Uptime`. |
| `system.asynchronous_metrics` | `metric, value, description`, per-core rows (`…CPU12`, `…MHz_12`) dropped (cloud: `hostname, metric, value`) | also drops per-object gauges (`DiskUsed_<disk>`, `Network*_<iface>`, `Block*_<dev>`) | `Uptime` (server, in seconds — the denominator for `system.errors`/`events`), `ReplicasMaxAbsoluteDelay`, `ReplicasSumQueueSize`, `NumberOfTables/Databases`, `MaxPartCountForPartition`, `TotalPartsOfMergeTreeTables`, `FilesystemCacheBytes`, `MemoryResident`. |
| `system.databases` | `name, engine, uuid` | hashed name; no uuid | Count of `Replicated` databases = one DDLWorker + one Keeper watch set per database per replica (a Keeper-load factor). `uuid` matches the `data/<uuid>/…` paths in exception messages. |
| `system.storage_policies` | `policy_name, volume_name, volume_priority, disks, max_data_part_size, move_factor, prefer_not_to_merge` | names hashed (same salt as `system.disks.name` and as `system.tables.storage_policy`, so the three files join) | Join `disks` with `system.disks.type` (`ObjectStorage` vs `Local`) to know whether table data lives on S3/Azure/GCS. |
| `system.zookeeper_connection` (≥ 23.8) | `name, host, port, index, connected_time, session_uptime_elapsed_seconds, is_expired, keeper_api_version, client_id, enabled_feature_flags` (cloud adds `hostname`) | name/host hashed; no port/client_id/flags | One row per Keeper connection. `session_uptime_elapsed_seconds` ≪ server `Uptime` = the session was re-established (expiry / Keeper leader change); `is_expired = 1` = no session now. Cloud: compare `host` and session age across replicas. |
| `system.replicated_fetches` | `database, table, elapsed, progress, result_part_name, partition_id, total_size_bytes_compressed, bytes_read_compressed, source_replica_hostname, source_replica_port, to_detached` (≤ 500, longest first; cloud adds `hostname`) | db/table/part/partition/source hashed; no port | Empty = nothing being fetched (healthy or nothing to catch up). Many long fetches = catching up after an outage. |
| `system.distributed_ddl_queue` (7 days) | `entry, entry_version, initiator_host, initiator_port, cluster, query (500), query_create_time, host, port, status, exception_code, exception_text (500), query_finish_time, query_duration_ms` (≤ 5000, newest first; read once in cloud) | hosts/cluster/query/exception hashed; no ports | One row per (entry, host). `status` ∈ Inactive/Active/Finished/Removing/Unknown. `entry` = `query-NNNNNNNNNN` (Keeper sequence). Comes from Keeper — slow or missing when Keeper is unhealthy; the collector's timeout then shows as code 159. |

### Windowed aggregates

| File | Grain / columns | Gov differences | Reading rules |
|---|---|---|---|
| `system.query_log_details_7_days` | 1-hour buckets × `query_kind, tables, database, table, type, user, interface, normalized_query_hash, exception_code`; sums `memory_usage, result_rows, result_bytes, written_bytes, written_rows, read_rows, read_bytes, query_duration_ms`; `count`, `query` (500 chars of one sample), `minDate, maxDate`, `exception`, `distinct_exceptions` | no `query`, `exception`, `distinct_exceptions`; identifiers hashed; `tables` is a hash of the `db.table` string | **`LEFT ARRAY JOIN tables`: a query touching N tables produces N rows and every sum is repeated on each. Filter to one `tables` value (or `tables = ''`) before summing; never sum across rows.** `type` includes `QueryStart` rows (no resource data) and `ExceptionBeforeStart`/`ExceptionWhileProcessing` — filter `type = 'QueryFinish'` for performance, `exception_code != 0` for failures. `distinct_exceptions` = how many different messages share that code (40 events / 1 distinct = one recurring fault). |
| `system.part_log_3_days` (3-day window) | 12-hour buckets (1-hour + `hostname` on ≥ 23.11) × `event_type, merge_reason, partition_id, table_name, error`; `exception, distinct_exceptions`; sums `peak_memory_usage, duration_ms, size_in_bytes`; `count` | separate hashed `database`,`table`; no exception columns | `event_type` names: NewPart, MergeParts, DownloadPart, RemovePart, MutatePart, MovePart, MergePartsStart, MutatePartStart. `merge_reason`: NotAMerge, RegularMerge, TTLDeleteMerge, TTLRecompressMerge. `error != 0` = failed operation (code in `error`). `duration_ms`/`size_in_bytes` are sums over the bucket — divide by `count` for averages. |
| `system.query_views_log_3_days` (3-day window) | 1-hour × `view_name, view_target, view_type, status, exception_code`; `executions, zero_write_executions`; sums `read_rows, read_bytes, written_rows, written_bytes, view_duration_ms`; `max_view_duration_ms`; `avg/median/max_peak_memory_usage`; `exception` (500 chars); `minDate, maxDate` (≥ 23.11 adds `hostname`) | no `exception`; `view_name`/`view_target` split into hashed `view_database`/`view_table` and `target_database`/`target_table` | One row per (bucket, view, status). `status` ∈ QueryStart, QueryFinish, ExceptionBeforeStart, ExceptionWhileProcessing. `zero_write_executions` counts executions with `read_rows > 0 AND written_rows = 0` — read as a ratio of `executions`, never absolute. **When an INSERT fails, EVERY view in the pipeline gets an `ExceptionWhileProcessing` row carrying the SAME message** — the culprit is the view named in the text ("while pushing to view X") and usually the one with the highest count. `view_uuid` is not collected: the server writes the zero UUID into it. Absent when `<query_views_log>` is not configured (the collector then reports code 636, not 60). |
| `system.metric_log_7_days` | 1-hour buckets: `time, avg_merge_pool_tasks, max_merge_pool_tasks, avg_fetch_pool_tasks, avg_interserver_connections, zk_transactions, zk_hw_exceptions, avg_memory_tracking_bytes` | same | The only server-side memory/background-pool time series in the bundle; **not rendered by the dashboard**. `avg_memory_tracking_bytes` ≈ tracked server memory; compare with `host_info.memory.total_bytes` or the cgroup limit. |
| `system.asynchronous_insert_log_7_days` | 1-hour × `database, table, status`; `flushes, total_rows (≥ 23.4), total_bytes, avg_flush_ms, p90_flush_ms` | hashed db/table | Absent on servers < 22.10 or when async inserts are unused. `status` ∈ Ok, ParsingError, FlushError. |
| `system.metric_log_coordination_3_days` | 1-hour buckets; columns chosen by **regex** over `metric_log` and therefore version-dependent: `sum(ProfileEvent_<X>)` for ZooKeeper*, Keeper*, S3*, DiskS3*, ReadBufferFromS3*, WriteBufferFromS3*, AzureBlobStorage*, ObjectStorage*, FilesystemCache*, CachedReadBuffer*, ReplicatedPart*, MergeTreeDataWriter*, InsertedParts, FailedQuery/FailedInsertQuery/FailedSelectQuery, Query/InsertQuery/SelectQuery, DelayedInserts, RejectedInserts, DistributedConnectionFail*, Merge, MergedRows, MergesTimeMilliseconds; `max(CurrentMetric_<Y>)` for ZooKeeperSession/Watch/Request, MemoryTracking, Query, ReadonlyReplica, ReplicatedFetch/Send/Checks, S3Requests, FilesystemCacheSize/Elements, Background*PoolTask, DelayedInserts, PartsActive/Outdated/Deleting, TCP/HTTP/InterserverConnection, DistributedFilesToInsert, Merge, Move | same | Column names literally contain the aggregate: `"sum(ProfileEvent_ZooKeeperHardwareExceptions)"`. A metric this version does not have is simply absent. Cloud: summed/maxed across replicas. |
| `system.text_log_histogram_1_day` | 1-hour × `level` × `logger_class`; `count`, `example` (300 chars) (cloud adds `hostname`; ≤ 10 000) | `logger_class` stays readable (a component name); a `<disk>::` prefix is masked to `*::`, and a fallback that still contains a dot (an unrecognised `db.table`) is hashed; `example` hashed | `logger_class` is the logger **normalised to its component**: `db.t (uuid)(PartsUpdateElection)` → `PartsUpdateElection`, `DDLWorker(db)` → `DDLWorker`, `db.t (ReplicatedMergeTreeQueue)` → `ReplicatedMergeTreeQueue`; plain loggers stay as they are. This is the file that says *when* Warning/Error volume changed and in which component. |
| `system.error_log_7_days` (≥ 24.8) | 1-hour × `code, error, remote`; `errors` (sum of per-minute counts; cloud adds `hostname`; ≤ 20 000) | same | The history of `system.errors`: every code from every thread per hour, including background operations `query_log` never sees. `remote = 1` = received from another server. |
| `system.text_log_keeper_1_day` | 1-hour × `marker` ∈ session_expired, session_finalized, connected, reconnecting, connection_loss, operation_timeout, other_keeper; `count, first_seen, last_seen, example` (200 chars; cloud adds `hostname`; ≤ 5000) | example hashed | All log levels, filtered to Keeper client lines — the *minute* of a session loss and the Keeper host in the `connected` example. Empty on servers whose `text_log` level is above Information. |
| `system.zookeeper_log_errors_1_day` (only when `<zookeeper_log>` is configured) | **failed Response rows only** (`PREWHERE type = 'Response' AND error != 'ZOK'`), 1-hour × `op_num` × `error`; `failed_requests`, `sessions_affected` (`uniq(session_id)`), `max_duration_ms` (cloud adds `hostname`; ≤ 20 000) | same | `error` ∈ `ZSESSIONEXPIRED`, `ZCONNECTIONLOSS`, `ZOPERATIONTIMEOUT` (loss), `ZNONODE`, `ZNODEEXISTS`, `ZBADVERSION` (ordinary control flow — high counts are normal). Successful volume and latency are **not** here: take them from `metric_log_coordination` (`ZooKeeperTransactions`, `ZooKeeperWaitMicroseconds / ZooKeeperTransactions`); session churn from `text_log_keeper_1_day` (`connected` per hour). Absent file = table not enabled. |
| `system.blob_storage_log_7_days` (≥ 23.11, only when `<blob_storage_log>` is configured) | 1-hour × `event_type` (Upload, Delete, MultiPartUpload*) × `disk_name` × `failed`; `count`, `bytes`, `example_error` (cloud adds `hostname`; ≤ 20 000) | disk/error hashed | `bytes` excludes Delete rows (their `data_size` is UInt64 max). A `Delete` hour followed by `FILE_DOESNT_EXIST`/`NoSuchKey` reads points at the deleted blob; `failed = 1` Uploads = writes that never reached the bucket. |
| `system.text_log` | rows, last **24 h**, `level IN (Warning, Error, Critical, Fatal)`, **LIMIT 2000**, ordered **severity first** (Fatal → Critical → Error → Warning), then newest, **at most 200 rows per (level, logger)**: `event_time, level, logger_name, message` (500 chars) | `logger_name` and `message` hashed | Since the severity-first ordering, a Warning flood can no longer hide the Error rows; still, 2000 rows can cover minutes on a noisy server — check the oldest `event_time` before concluding anything about the window. `text_log` is disabled by default in OSS; an absent file usually means that. |
| `text_log_<ts>` (opt-in slice) | full rows for an explicit `--from/--to` window and `-text-log-level` | rejected in gov | Includes `message_format_string` on ≥ 23.1 — group by it to collapse variants. |

### `query_analysis/` (only with `--query-id` / `--normalized-query-hash`; never in gov)

| File | Answers |
|---|---|
| `query_details` | the full `query_log` row of the focus execution: durations, memory, read/written rows, `tables`, `exception`, `query`, `ProfileEvents` map (+ `query_cache_usage` ≥ 23.8, `peak_threads_usage` ≥ 23.9) |
| `profile_events` | `metric, value` — all ProfileEvents of the focus execution, largest first |
| `text_log_parts` | the "Selected N/M parts by partition key, K marks…" and "Reading approx. N rows with M streams" lines — did it prune or full-scan? |
| `text_log_full` | every text_log row of the focus query (≤ 5000) |
| `tables_for_query` | current DDL and size of each table the query touched |
| `fast_slow_query_ids` | slowest vs fastest execution of the same `normalized_query_hash` in the window |
| `profile_events_compare` | `metric, slow_value, fast_value, delta, percentage_diff` — the most diagnostic file |
| `hash_by_host` | per-host `executions, max/min/avg/p95 duration, avg/max memory, errors` — one slow node? |
| `hash_summary` | per-minute `executions, succeeded, failed` |
| `failed_over_time` | per-minute failures by `error_type` ("NAME (code)") |
| `failed_queries` | failures by tables × error type × user, with `sample_exception`, `sample_query` |
| `executions_timeline` | one row per execution (≤ 10 000): `ts, query_id, type, exception_code, query_duration_ms, memory_usage, user_cpu_us, read_rows, read_bytes, hostname, user` |

## 5. `host_info.json`

```
collected_at                      RFC3339 UTC
os        { available, hostname, distro, distro_version, kernel_version, kernel_full, arch, go_os, uptime_seconds }
cpu       { available, logical_cpus, model_name, vendor_id, mhz, load_avg_1_5_15: [s,s,s], notable_flags: [avx2|avx512f|asimd|sve…] }
memory    { available, total_bytes, free_bytes, available_bytes, buffers_bytes, cached_bytes, swap_total_bytes, swap_free_bytes }
disks     [ { device, mount_point, fs_type, total_bytes, free_bytes, used_pct } ]      # includes tmpfs/overlay; filter real mounts
top_processes_by_rss [ { pid, ppid, state, rss_bytes, threads, command } ]              # ≤ 25
clickhouse_relevant_tunables { transparent_hugepages, transparent_hugepages_defrag, vm_swappiness, vm_overcommit_memory,
                               vm_max_map_count, fs_nr_open, fs_file_max, cgroup_memory_limit_bytes, cgroup_cpu_max,
                               clickhouse_open_files_soft, clickhouse_open_files_hard, clickhouse_nproc_soft, clickhouse_process }
notes     [ "…what could not be read and why…" ]
```
Fields are omitted when unreadable; `available: false` + `notes` marks a degraded section (typical when the tool ran on a laptop against a remote server — then the host facts describe the **wrong machine**; say so and ignore them).

## 6. `logs/`

- `clickhouse-server.log`, `clickhouse-server.err.log` (and rotated `*.gz`/`*.zst` only with `-logs-include-archives`). Files from several directories are flattened; name collisions get `-2`, `-3` suffixes.
- Files above `-logs-max-mb` (default 50 MiB) are **tail-truncated** and start with:
  `### support-diagnostic: TRUNCATED — original N bytes, kept last M bytes (tail) ###`
  The first surviving line is *not* the start of the log. Check for this header before reasoning about "when did it start".
- Line format: `2026.08.25 12:06:56.858337 [ thread ] {query_id} <Level> Logger: message`. Grep, don't read top-to-bottom.

## 7. `configuration/`

Mirror of the config directory (`config.d/…`, `users.d/…`, sometimes `config.xml`). Credentials, keys, tokens, PEM blocks, long hex/base64 blobs are replaced; **hostnames, IPs, cluster topology, macros, paths, table names and every performance setting are kept**. Files that failed to parse were skipped (fail-closed), so a missing file is not proof a setting is unset.

## 8. `dashboard.html`

Self-contained; all panel data is embedded as one JSON literal on a line starting `const DATA = {`. Keys: `alerts` (full rule results **including matched rows**), `keeper_metric_hourly` (per hour: `transactions, hw_exceptions, user_exceptions, wait_us, sessions_min, sessions_max` — the Keeper health test inputs, 7 days; **check `keeper_wait_available` / `keeper_session_available` first** — when either is `false` the server's version does not export that counter and the generator substituted `0`, which is indistinguishable from a real zero, so never read `sessions_min = 0` as a lost session or `wait_us = 0` as zero latency without them), `keeper_errors_hourly` (`time, code_name, count` for 999/242/319/571/252; `keeper_errors_source` says whether it came from `error_log` or `query_log`), `keeper_connection` (live `zookeeper_connection` rows), `version, uptime, total_databases, total_tables, active_parts, total_size`, `host_info`, `host_checks`, `storage_by_db`, `engines_dist`, `tables_list`, `query_by_time`, `query_by_kind`, `query_slow`, `query_heavy`, `query_by_user`, `exceptions`, `part_log_by_time`, `part_log_by_type`, `dictionaries`, `crash_log`, `mutations`, `detached`, `replication_queue`, `clusters`, `replicas`, `disks`, `text_log`, `bundle_files`, `server_errors`, `high_part_count`, `ttl_activity`, `async_inserts`, and `qa_*` when query analysis ran. Extraction recipe in `reading-recipes.md`.

## 8a. `execution_log.txt`

Plain text, written just before the archive. Header (`started`, `finished`, `mode`, `server`, `target`, `window`, `timeout`, `format`), a **Summary** (`collectors: N ok, M failed, K empty — T of query time`, alert counts, phase wall times), **Most expensive collectors** (top 10 by wall time with bytes and rows), **Failed collectors** with the ClickHouse error text (capped at 300 chars), then **All entries** as a pipe table — `| # | stage | name | source | status | duration_ms | bytes | rows | note | error |` — where `stage` ∈ collector / alert / text_log / analysis, `source` is the version directory (`root` = the base file) and `note` carries the output file name or the alert instance count. Read it **first**: a result file that is missing was a `failed` collector (its error is here), not an empty table; a collector with code 159 hit the tool's own `-query-timeout`; a collector taking tens of seconds on a small server is a window worth shortening. Bundles from tool builds before this file exist without it.

## 9. `alerts_summary.json`

```json
{ "evaluated": 8, "fired": 1, "errored": 2, "not_applicable": 1, "note": "...",
  "rules": [ { "name": "too_many_parts", "title": "...", "severity": "warning", "file": "too_many_parts.yaml",
               "state": "fired|clean|skipped|error", "instance_count": 12,
               "skip_reason": "...", "error": "...", "checked_at": "..." } ] }
```
Matched rows are **never** included — `instance_count` is the substitute. `evaluated` excludes skipped and errored rules. An `error` state is *not* a finding (rule could not run: missing column on this version, missing grant); a `skipped` state means the system table does not exist here.

Rule thresholds are in `health-checks.md` §1 so you can re-evaluate them yourself from the JSONL when no summary is present.

## 10. Gov mode specifics

- Every `database`, `table`, `name`, `user`, hostname-like value is `hex(SHA256(value || salt))`. Hashes are **stable within one salt**, so you can still join files on them and count per table.
- The mapping CSV (`database,table,database_hash,table_hash`) exists only on the collector's machine. If the user has it, offer to resolve hashes **locally**; never ask for the salt.
- Not available in gov: dashboard, query analysis, query text, exception text, config, host facts, logs, `--collect-text-log`, `crash_log`, `stack_trace`. Say which findings are therefore out of reach instead of guessing.
