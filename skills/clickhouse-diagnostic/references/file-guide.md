# File guide — what each bundle file is for, what to read first, what healthy looks like

One entry per file the collector produces. Read the entry **before** the column table in `bundle-layout.md` §4; it tells you the question the file answers, the first three things to compute, what a healthy server shows, the red flags (with the check/pattern id), the traps, and which other files to pair it with. Thresholds marked *(guideline)* are field experience, the others are the tool's alert rules or ClickHouse defaults.

Windows: `*_7_days` files cover the last 7 days (or `-from/-to`); `system.text_log` covers 24 h capped at 2000 rows; every other file is a snapshot taken at collection time.

---

## Point-in-time snapshots

### system.version
**Why we run it:** every threshold, default and bug-fix statement is version-dependent; without it the rest of the bundle cannot be interpreted safely.
**Question:** which exact server build produced this bundle?
**Read first:** the single `version` value (`MAJOR.MINOR.PATCH.BUILD`).
**Healthy looks like:** a recent patch of an LTS minor (`YY.3`, `YY.8`) or the current stable.
**Red flags:** a `.1`/`.2` patch of a minor (contains none of that minor's bug fixes); a minor older than ~12 months (out of support); versions differing across nodes in `system.clusters`/logs banners (P-31, P-54).
**Traps:** Cloud builds (`26.2.1.558`-style) may include changes not in the OSS tag.
**Pairs with:** everything — every "default is X" statement depends on it (`clickhouse-source.md`).

### system.clusters
**Why we run it:** distributed and replicated setups fail in the gaps between nodes — a stale host or a node with `errors_count > 0` explains `ON CLUSTER` timeouts and fetch failures before any table is opened.
**Question:** what topology does this node believe it is part of, and is every peer reachable?
**Read first:** distinct `cluster` names; rows per cluster (`shard_num`, `replica_num`); `errors_count`, `slowdowns_count`, `estimated_recovery_time`; `is_local`.
**Healthy looks like:** every cluster with the expected shards×replicas, `errors_count = 0`, exactly one `is_local = 1` row per cluster.
**Red flags:** `errors_count > 0` (distributed queries recently failed to that host — HC-3.6, P-44); hosts that no longer exist (`ON CLUSTER` timeouts, P-21/P-42); a single-node cluster on a server that claims replication.
**Traps:** in cloud mode the `default` cluster is the service's replica set; hashed `host_name` in gov; `is_active`/`database_replica_name` only on ≥ 23.5.
**Pairs with:** `configuration/` `<remote_servers>`, `system.replicas`, `system.replication_queue`.

### system.tables
**Why we run it:** nearly every parts/merge/insert finding is *explained* by the schema — the partition key, the sorting key, the engine, the MVs attached to a source and the per-table `SETTINGS` overrides.
**Question:** what does the schema look like — engines, keys, partitioning, MVs, per-table settings?
**Read first:** non-system tables by `engine`; `partition_key` and `sorting_key` of the tables that show up in parts/queries findings; count of `MaterializedView` per source (`dependencies_table`); `engine_full` for `SETTINGS` overrides (`parts_to_throw_insert`, `ttl_only_drop_parts`, `min_age_to_force_merge_seconds`, `storage_policy`).
**Healthy looks like:** MergeTree-family tables with a sorting key that starts with low-cardinality filter columns, partition keys at day/month granularity, ≤ ~10 MVs per source, TTL declared on log-like tables.
**Red flags:** `sorting_key = ''`/`tuple()` on large tables, high-cardinality partition keys (P-03, P-50); dozens of MVs on one source (P-34); `TTL` in DDL but no TTL activity in `part_log` (P-18); `Nullable` key columns; `Join`/`Buffer`/`Kafka` engines involved in a hang (P-21).
**Traps:** in gov only 8 columns survive (no DDL/keys); `create_table_query` carries comments and defaults — never paste it verbatim outside the machine (`privacy.md`).
**Pairs with:** `system.parts` (explain counts), `system.columns` (types), `query_log_details` (which tables are hot).

### system.columns
**Why we run it:** types drive merge memory, compression and MV compatibility; the failing column named in an error message is looked up here.
**Question:** which types are in use, and how wide are the tables?
**Read first:** columns per table (`count()`); types worth flagging: `Nullable(...)` in keys, `String` where `LowCardinality`/Enum fits, `JSON`/`Object`/`Dynamic`/`Variant`, `Map(String, String)` used as a catch-all, `FixedString` mismatches between MV and target.
**Healthy looks like:** compact typed schemas; Nullable only where NULL is meaningful.
**Red flags:** JSON/Object columns on tables whose merges OOM or crash (P-11, P-30); `String` columns for bounded value sets (P-50); type differences between an MV's `as_select` output and its target (P-32).
**Traps:** large file (one row per column); `default_expression` omitted in gov; there are no per-column size statistics here (only in `system.parts_columns`, not collected).
**Pairs with:** `system.tables`, `part_log`/`crash_log` when a column name appears in an error message.

### system.parts
**Why we run it:** part count per partition is the single best indicator of MergeTree health — it predicts `TOO_MANY_PARTS`, slow reads and mutation backlogs before they become errors.
**Question:** is the MergeTree healthy — how many parts, how big, how fragmented, where do they live?
**Read first:** filter `active = 1`; parts per `(database, table, partition_id)`; `countIf(level = 0)`; `avg(rows)` per part; `sum(bytes_on_disk)` per database; `data_uncompressed_bytes / data_compressed_bytes`.
**Healthy looks like:** tens of parts per partition, level-0 parts a small minority, avg rows/part ≥ 100k on busy tables, compression ratio > 3, `system` database small.
**Red flags:** > 300 parts in one partition (HC-2.1, P-01), hundreds of level-0 parts (HC-2.2), avg rows < 10k (HC-2.3, P-02), a part > 150 GiB (HC-2.9), `system` among the largest databases (HC-4.4, P-17), `delete_ttl_info_*` = 1970 on tables with TTL (P-18), `is_frozen = 1` leftovers, `disk_name` pointing at a cold volume unexpectedly (P-45).
**Traps:** current bundles hold **active parts only, largest first, capped at 50 000 rows** — exactly 50 000 rows means the smallest parts were cut off (the per-partition counts for small-part tables may be understated); bundles from before September 2026 also include inactive parts — filter `active = 1` regardless; `partition` (value) is hashed in gov while `partition_id` is not; single replica even in cloud mode.
**Pairs with:** `system.tables` (keys explain counts), `part_log` (are merges happening), `system.merges` (are they stuck), `system.mutations` (what pins them).

### system.settings and system.server_settings (≥ 23.4 for server_settings on onprem/gov)
**Why we run it:** "which settings deviate from defaults" used to require `configuration/`, which cloud bundles never have and gov never ships; these two tables answer it directly, with the ClickHouse default next to the value.
**Question:** what has been tuned away from defaults — query/profile settings (`system.settings`) and server-level ones (`system.server_settings`)?
**Read first:** `WHERE changed = 1` in both files; memory (`max_memory_usage`, `max_server_memory_usage*`, `max_bytes_before_external_group_by`), pools (`background_pool_size`, `background_fetches_pool_size`, `background_merges_mutations_concurrency_ratio`, `number_of_free_entries_in_pool_to_execute_mutation`), concurrency (`max_concurrent_queries*`), caches (`mark_cache_size`, `uncompressed_cache_size`), inserts (`async_insert*`, `parallel_distributed_insert_select`), `compatibility`; `keep_free_space_bytes`, `max_table_size_to_drop`.
**Healthy looks like:** a short `changed = 1` list that matches what the owner knows they set; server memory ratio ≤ 0.9; pools sized to the CPUs.
**Red flags:** coupled settings changed on one side (P-16); `max_server_memory_usage_to_ram_ratio` > 0.9 or `max_memory_usage` per query above the server limit (P-10); `compatibility` set to an old version after an upgrade (P-54); `parts_to_throw_insert`-style insurance never reverted (P-01); `wait_for_async_insert = 0` (P-33); `max_concurrent_queries` lowered (P-20); `merge_tree` overrides that only exist on some versions.
**Traps:** `system.settings` reflects the **collector's user/profile**, not every user — per-user profiles live in `configuration/users.d`; MergeTree table settings are **not** here (see `system.tables.engine_full` and, if needed, ask for `system.merge_tree_settings`); in gov the identifying `server_settings` values read `REMOVED` but the name and `changed` flag survive; on < 23.4 there is no `default` column and no `server_settings` file — verify a setting's default in source at the tag (`clickhouse-source.md`).
**Pairs with:** `configuration/` (cross-check), `metric_log` (do the pools actually saturate), `host_info` (cgroup limit vs `max_server_memory_usage`).

### system.detached_parts
**Why we run it:** detached parts are the server's own record of data it could not trust or re-attach; their `reason` distinguishes corruption from replication bookkeeping, and they still occupy disk.
**Question:** has anything been set aside as broken, unexpected or manually detached — and is it wasting disk?
**Read first:** `count()` and `sum(bytes_on_disk)` by `database, table, reason`; `modification_time` (≥ 23.11) to date the event.
**Healthy looks like:** empty file.
**Red flags:** `reason IN ('broken', 'broken-on-start', 'covered-by-broken')` → corruption or version skew (HC-2.11, P-31); `unexpected`/`ignored`/`clone` in large numbers after a replication incident (P-41/P-43); many GiB detached on a full disk (HC-4.1).
**Traps:** root variant on < 22.11 has no `bytes_on_disk`; gov drops `disk`/`path`; `reason = ''` = manual `DETACH`.
**Pairs with:** `system.errors` 40/226/246, `logs/` around `modification_time`, `system.disks`.

### system.disks
**Why we run it:** a full disk turns every other symptom (merge failures, read-only replicas, insert errors 243) into one root cause; the check is cheap and decisive.
**Question:** how much room is left on each volume ClickHouse can write to?
**Read first:** `name`, `type`, `free_pct`; `unreserved_space` vs `free_space` (reservations by running merges).
**Healthy looks like:** > 30 % free on the data volume(s) *(guideline)*, `type = 'local'` for hot data unless S3 tiering is intended.
**Red flags:** `free_pct < 15` (critical, HC-4.1), `< 5` (inserts/merges failing with 243); an object-storage disk when 499 errors are present (P-45); a cold disk receiving fresh data (P-45 `move_factor`).
**Traps:** `free_space`/`total_space` are human-readable strings — use `free_pct` or parse; one row per replica in cloud; `unreserved_space` only on ≥ 22.10.
**Pairs with:** `host_info.disks` (mount-level view), `system.parts` (what fills it), `configuration/` `<storage_configuration>`.

### system.errors
**Why we run it:** the fastest triage signal — one row per error code with counts and last occurrence, so the dominant failure family is known before reading any log.
**Question:** which error codes has this server raised since it started, how often, and when last?
**Read first:** top 10 by `value`; `last_error_time` relative to the collection time; `last_error_message` for the top codes (map codes with `error-codes.md`).
**Healthy looks like:** low counts of client-side codes (47, 60, 62) and nothing recent in the memory/Keeper/parts families.
**Red flags:** 241 (P-10), 252 (P-01), 999/242 (P-40), 243 (HC-4.1), 173/76 (P-13), 49/1001 (P-30), 40/226 (P-31), 499 (P-45), 202 (P-20) — especially with `last_error_time` inside the incident window.
**Traps:** **cumulative since server start** — 10 000 hits over 90 days of uptime is not 10 000 in 3 hours; pair with uptime (dashboard `uptime` or `host_info.os.uptime_seconds` if the server restarted with the host). Message hashed and trace empty in gov.
**Pairs with:** `query_log_details.exception_code` (which queries), `part_log.error` (which merges), `text_log`/`logs/` (context).

### system.merges
**Why we run it:** a merge that is running for hours or is near the memory limit is the usual reason a part backlog grows while the pool looks idle; only a live snapshot can show it.
**Question:** what is merging right now, and is any merge stuck or oversized?
**Read first:** `elapsed`, `progress`, `num_parts`, `total_size_bytes_compressed`, `memory_usage`, `is_mutation`, `merge_algorithm`.
**Healthy looks like:** a handful of merges each well under an hour with `progress` moving; empty on an idle server is fine.
**Red flags:** `elapsed` in hours with `progress` < 0.5 (HC-2.5, P-04); `memory_usage` near limits (P-11); `is_mutation = 1` merges hogging slots (P-05/P-14); the same `result_part_name` seen failing in `part_log`.
**Traps:** an **empty file means nothing was merging at that instant**, not that merges are broken; a snapshot cannot show throughput — use `part_log` for that.
**Pairs with:** `part_log` (history), `system.mutations` (`parts_to_do_names` ∩ merge sources), `metric_log` (pool saturation).

### system.mutations
**Why we run it:** pending ALTER/DELETE/UPDATE work blocks DDL, pins parts and consumes merge capacity; the backlog and its age are visible only here.
**Question:** which ALTER/DELETE/UPDATE operations are still pending, for how long, and are they blocked?
**Read first:** count per table; age = collection time − `create_time`; `parts_to_do`; `command` (heavy `MODIFY COLUMN`? row-level `DELETE`/`UPDATE _row_exists`?); `parts_to_do_names` vs `system.merges`.
**Healthy looks like:** empty, or a few recent mutations with `parts_to_do` decreasing.
**Red flags:** older than 3 h with `parts_to_do > 0` (HC-8.1, P-14); > 100 on one table (HC-8.2, P-15/P-05); `is_killed = 1` still present (cloud; normal for a while, P-14); commands converting types on data with NULLs (P-15).
**Traps:** finished mutations are pruned by the server, so the file is the backlog only; `latest_fail_reason` is **not** collected — a failing mutation shows as "old with parts_to_do" plus `part_log` `MutatePart` errors; `is_killed` exists only in the cloud variant; `command` is hashed in gov.
**Pairs with:** `part_log` (`event_type = 'MutatePart' AND error != 0`), `system.merges`, `system.parts` (are the named parts huge?).

### system.replicas
**Why we run it:** a replicated cluster under load degrades replica by replica: read-only state, expired Keeper sessions and `absolute_delay` show which replica is falling behind and whether writes are still accepted.
**Question:** is replication healthy on every `Replicated*` table — connected to Keeper, writable, caught up?
**Read first:** `is_readonly`, `is_session_expired`, `absolute_delay`, `queue_size` split into `inserts_in_queue`/`merges_in_queue`/`part_mutations_in_queue`, `parts_to_check`, `active_replicas`/`total_replicas`, `queue_oldest_time`, then `zookeeper_exception`/`last_queue_update_exception` for the reason behind a read-only or stalled replica.
**Healthy looks like:** `is_readonly = 0`, `absolute_delay` a few seconds, `queue_size` small and draining, `active_replicas = total_replicas`.
**Red flags:** `is_readonly = 1` (critical, HC-1.4, P-40), `absolute_delay > 60` (HC-3.1, P-41), `active_replicas < total_replicas` (HC-3.3 — a dead replica or a ghost in Keeper, P-43), `parts_to_check > 0` (suspicious parts), `queue_oldest_time` hours old, non-empty `zookeeper_exception` (Keeper-side failure — pairs with `system.errors` 999/242) or `last_queue_update_exception` (the replica cannot refresh its own queue).
**Traps:** empty file = no replicated tables (normal on a single node); one row per table per replica; the file is sorted by `absolute_delay DESC`, so the first row is the worst; `zookeeper_exception` and `last_queue_update_exception` are hashed in gov (empty stays empty, so "is this replica erroring at all?" is still visible).
**Pairs with:** `system.replication_queue` (why), `metric_log.zk_*` (Keeper health), `system.errors` 999/242.

### system.replication_queue
**Why we run it:** when a replica falls behind, the queue says *why* — fetch-bound (`GET_PART`) vs merge-bound (`MERGE_PARTS`), the postpone reason and the last exception; any type above ~60 entries means a replica is becoming unavailable *(guideline)*.
**Question:** what is the replica waiting to do, and why is it not doing it?
**Read first:** `count()` by `type` (GET_PART = fetch-bound, MERGE_PARTS = merge-bound, MUTATE_PART, ALTER_METADATA); `postpone_reason` values; `last_exception` non-empty rows; `num_tries` on those rows; oldest `create_time`; `is_currently_executing` count.
**Healthy looks like:** empty, or a few entries with recent `create_time` and empty `last_exception`.
**Red flags:** any `type` > 60 entries *(guideline)* (HC-3.4), `last_exception` with 232/234 (part missing on source → P-41/P-43), 210/209 (fetch network failures → P-44), 40/226 (corrupt source → P-31); `postpone_reason` "N fetches already executing, max N" (fetch pool too small, P-41), "because part … is not ready" chains; a high `num_tries` on an entry that is still queued (retried and failing, not merely waiting its turn).
**Traps:** `database`, `table`, `replica_name`, `postpone_reason` and `last_exception` are hashed in gov (empty stays empty, so "has an exception" is still visible); `num_tries` is not hashed.
**Pairs with:** `system.replicas`, `configuration/` (`background_fetches_pool_size`, `<interserver_http_host>`), `system.clusters.errors_count`.

### system.processes
**Why we run it:** what was running at collection time — a stuck `BACKUP`, `OPTIMIZE FINAL` or runaway query is otherwise invisible in aggregated logs.
**Question:** what was running at the moment of collection?
**Read first:** `elapsed`, `memory_usage`, `read_rows`/`total_rows_approx` (progress), `user`, `is_cancelled`.
**Healthy looks like:** the collector's own query plus a few short-lived queries.
**Red flags:** queries with `elapsed` in hours (stuck `BACKUP`, `OPTIMIZE FINAL`, `ALTER`, a runaway `SELECT`), `is_cancelled = 1` rows that never finished, many rows from one user (P-20/P-55).
**Traps:** a single snapshot — absence proves nothing about the incident; `query_id` lets you cross-reference `query_analysis/` if collected.
**Pairs with:** `query_log_details` (history of the same hashes), `system.merges`.

### system.dictionaries
**Why we run it:** a failed or oversized dictionary silently breaks every query that calls `dictGet` and can hold a large share of RAM.
**Question:** are external dictionaries loaded, fresh and reasonably sized?
**Read first:** `status`, `last_exception`, `bytes_allocated` (sum vs RAM), `element_count`, `hit_rate`/`found_rate`, `loading_duration`, `last_successful_update_time` vs `lifetime_max`.
**Healthy looks like:** all `LOADED`, `last_exception = ''`, total `bytes_allocated` a small share of RAM, updates within `lifetime_max`.
**Red flags:** `FAILED`/`FAILED_AND_RELOADING` (HC-9.1 — source unreachable, credentials, schema), `LOADING` at collection (HC-9.2), `bytes_allocated` in tens of GiB (HC-9.3, P-12), `cache`-layout dictionaries with low `hit_rate`, stale `last_successful_update_time`.
**Traps:** gov drops `source`/`last_exception`/`comment`; per-replica state in cloud (one row per replica).
**Pairs with:** `query_log_details` 156/36 errors, `configuration/` dictionary definitions.

### system.crash_log
**Why we run it:** a crash is the highest-severity event a server can record; the trace and the triggering `query_id` are needed to search for a fix.
**Question:** did the server die with a fatal signal, when, and doing what?
**Read first:** `event_time`, `signal` (11 SIGSEGV, 6 SIGABRT, 7 SIGBUS), `version`, `query_id`, first frames of `trace_full`.
**Healthy looks like:** 0-byte file (the file exists, empty).
**Red flags:** any row = critical (HC-1.1, P-30); identical top frames across rows = deterministic trigger; `query_id` set = a query caused it (find its hash in `query_log_details`).
**Traps:** not collected in gov; OOM-kills by the OS leave **no** row here (look for restarts in `logs/` instead, P-13); on cloud the table exists only on the replica that crashed.
**Pairs with:** `logs/` `<Fatal>` blocks (full symbolised stack), `system.version`, `query_log_details`.

### system.stack_trace
**Why we run it:** for a hang in progress, thread stacks are the only way to see which lock or external call everyone is waiting on.
**Question:** what was every thread doing at collection time — useful only for a hang that is happening right now.
**Read first:** thread count; group by `thread_name`; look for many threads in the same lock/wait frame.
**Healthy looks like:** a few hundred threads mostly idle in pool waits.
**Red flags:** hundreds of threads in the same mutex/condition-variable frame (a deadlock or lock convoy, P-22); threads stuck in Keeper client calls (P-40) or S3 I/O (P-45).
**Traps:** not collected in gov; large, symbol-heavy, identifies the build; only meaningful if the bundle was taken **during** the hang.
**Pairs with:** `system.processes`, `system.merges`.

### system.metrics, system.events, system.asynchronous_metrics
**Why we run it:** the hourly aggregates cannot say what is true *now*, and `system.errors` has no denominator; these three are the live gauges, the cumulative counters and the server `Uptime`.
**Question:** does this server hold a Keeper session right now; are replicas read-only; how many fetches/merges are running; how many object-storage and Keeper errors has it seen since start, per hour of uptime?
**Read first:** `metrics`: `ZooKeeperSession`, `ZooKeeperWatch`, `ReadonlyReplica`, `ReplicatedFetch`, `Background*PoolTask`, `S3Requests`, `FilesystemCacheSize`, `MetadataFromKeeperCacheObjects` (cloud: per `hostname`); `asynchronous_metrics`: `Uptime`, `ReplicasMaxAbsoluteDelay`, `ReplicasSumQueueSize`, `MaxPartCountForPartition`, `NumberOfTables`, `NumberOfDatabases`; `events`: `ZooKeeperHardwareExceptions`, `ZooKeeperUserExceptions`, `S3ReadRequestsErrors`, `ReadBufferFromS3RequestsErrors`, `ReplicatedPartFailedFetches`, `RejectedInserts`, `DelayedInserts`, `FailedQuery`.
**Healthy looks like:** `ZooKeeperSession ≥ 1`, `ReadonlyReplica = 0`, exception and error counters tiny relative to `Uptime`, `MetadataFromKeeperCacheObjects` similar across replicas.
**Red flags:** `ZooKeeperSession = 0` (HC-3.12); `ReadonlyReplica > 0` (HC-1.4); a replica whose `MetadataFromKeeperCacheObjects` is ≈ 1 while peers hold 10⁵ (P-58); `ZooKeeperHardwareExceptions` in the millions (P-57); `S3*RequestsErrors` growing (HC-4.10); `Uptime` of minutes = the server restarted (HC-1.2).
**Traps:** `events` and `system.errors` are cumulative since start — divide by `Uptime`, never treat as rates; `asynchronous_metrics` refresh every minute, so `Uptime` is ≤ 60 s stale; per-core rows are dropped on purpose; gov drops per-object gauges.
**Pairs with:** `system.errors` (same counters, with messages), `metric_log_coordination_3_days` (the same counters over time), `zookeeper_connection`.

### system.databases and system.storage_policies
**Why we run it:** `system.tables` cannot tell `Atomic` from `Replicated` databases, and it names a `storage_policy` without saying what backs it.
**Question:** how many `Replicated` databases exist (a Keeper-load multiplier); do user tables live on object storage?
**Read first:** `databases`: count by `engine`; `storage_policies`: `disks` of the policies that `system.tables.storage_policy` uses, joined with `system.disks.type`.
**Healthy looks like:** a handful of `Replicated` databases; policies whose disks you can name.
**Red flags:** hundreds of `Replicated` databases × tens of replicas when Keeper is the finding (HC-3.13, P-57); `ObjectStorage` disks behind user tables when 107/`NoSuchKey` errors appear (HC-4.0, P-58).
**Traps:** `Replicated` here is the *database* engine, unrelated to `ReplicatedMergeTree` tables; gov hashes names with the same salt as `system.tables`/`system.disks`, so the joins still work.
**Pairs with:** `system.tables` (`storage_policy`, engines), `system.disks`, `configuration/` `<storage_configuration>`.

### system.zookeeper_connection (≥ 23.8)
**Why we run it:** the only place that says which Keeper node this server talks to and **how old the session is**.
**Question:** is there a Keeper session right now, when was it (re)established, is the server pinned to one Keeper host?
**Read first:** `is_expired`, `connected_time`, `session_uptime_elapsed_seconds` vs `Uptime` (`asynchronous_metrics`), `host`/`index`; cloud: the same per `hostname`.
**Healthy looks like:** one row per configured connection, `is_expired = 0`, session age ≈ server uptime.
**Red flags:** `is_expired = 1` (critical, HC-3.9); session age of minutes/hours on a long-running server = the last expiry happened at `connected_time` (P-40/P-57); all replicas on the same `host` (leader-only traffic) or one replica on a different host than its peers.
**Traps:** absent on < 23.8 (not collected, not evidence); `session_uptime_elapsed_seconds` resets on every reconnect, so it dates the *last* expiry only.
**Pairs with:** `metric_log_coordination` (the hours), `zookeeper_log_errors_1_day`, `configuration/zookeeper.xml`.

### system.replicated_fetches
**Why we run it:** fetches in flight are invisible in `part_log` until they finish or fail.
**Question:** is this replica pulling parts right now, from whom, how big, stuck?
**Read first:** `elapsed`, `progress`, `total_size_bytes_compressed`, `source_replica_hostname`.
**Healthy looks like:** empty, or a few fetches with moving `progress`.
**Red flags:** dozens of long `elapsed` fetches after an incident (catching up — expected, but it explains `absolute_delay`); a fetch whose `progress` is identical in two bundles (wedged interserver connection, HC-3.1/P-41); `to_detached = 1` (fetching into `detached/` for a check).
**Traps:** a snapshot; empty proves nothing about the last hour — use `part_log` `DownloadPart` for history.
**Pairs with:** `system.replicas` (`queue_size`, `inserts_in_queue`), `part_log` `DownloadPart` errors (499, 210, 209).

### system.distributed_ddl_queue (7 days)
**Why we run it:** `ON CLUSTER` and Replicated-database DDL execute through Keeper, not through `query_log` on the initiator; their per-host status and failures live only here.
**Question:** which DDL is stuck or failed, on which host, why; was DDL replayed after a Keeper incident?
**Read first:** `status != 'Finished'` rows and their age; `exception_code` histogram (57, 571, 999, 60, 253); the same `query` text under many `entry` values.
**Healthy looks like:** every entry `Finished` on every host within seconds, `exception_code = 0`.
**Red flags:** `Active`/`Inactive` entries older than minutes (HC-3.11 — a host that never picked up the task, often one that is down or has lost its session); bursts of 57 `Mapping for table with UUID … already exists` on `CREATE OR REPLACE TABLE … .tmp.inner_id.*` (P-57: Replicated databases replaying materialized-view DDL); 571 (session died mid-replay).
**Traps:** read from Keeper — when Keeper is unhealthy this collector is slow or times out (code 159 in `query_log` for the collector user, HC-0); at most 5000 newest rows; one row per (entry, host), so a 29-replica cluster has 29 rows per statement; cloud reads it once (it is the same from every replica).
**Pairs with:** `system.databases` (Replicated engines), `query_log_details` `Create` rows with empty `user`, `text_log_histogram` `DDLWorker`.

## Windowed aggregates

### system.query_log_details_7_days
**Why we run it:** the workload is the first thing to rule in or out — traffic shape, failing codes, the slow and heavy patterns and who runs them; most incidents start here (Layer 1: traffic and behaviour) before data model or infrastructure.
**Question:** what workload did the server run over the window — how much, by whom, how slow, how much memory, and what failed?
**Read first:** hourly `count` by `query_kind` (traffic shape and spikes); `exception_code != 0` totals and share (`distinct_exceptions` says how many different messages hide behind a code); top `normalized_query_hash` by `query_duration_ms/count`, `read_bytes/count`, `memory_usage/count`; `count` by `user`.
**Healthy looks like:** a flat or diurnal query rate, < 1–2 % exceptions (mostly client-side codes), slow patterns explained by size, no single user dominating unexpectedly.
**Red flags:** 241 share (HC-5.3, P-10), 252 on inserts (P-01), 202 (P-20), 159/394 clusters (P-21), 999 (P-40); one hash with extreme `read_bytes/count` or `FINAL`/`SELECT *` in `query` (P-50/P-51); inserts in the thousands per hour with tiny `written_rows/count` (P-02); a monitoring user on top (P-55).
**Traps:** **`LEFT ARRAY JOIN tables` duplicates each group once per table with all sums repeated — fix one `tables` value (or `tables = ''`) before summing, never re-sum across rows**; `type = 'QueryStart'` rows carry no resource data; every metric is a **sum over the hour bucket** — divide by `count`; `query` is a 500-char sample of one query; in gov there is no `query`/`exception` and identifiers are hashed.
**Pairs with:** `query_analysis/` (drill into one hash), `system.tables` (schema of the hot tables), `metric_log` (memory in the same hours), `system.errors`.

### system.part_log_3_days
**Why we run it:** the history of background work — it shows whether merges keep up with inserts, how big inserts are, and which merges/mutations fail with which code; `system.merges` alone is a single instant.
**Question:** what did background work do over the window — parts created, merged, mutated, fetched, removed — and did any of it fail?
**Read first:** `event_type × merge_reason` totals; `error != 0` rows with `exception`/`distinct_exceptions`; `NewPart` per hour per table vs `MergeParts` per hour; `size_in_bytes/count` for NewPart (insert size) and MergeParts (merge size); `peak_memory_usage` maxima per table.
**Healthy looks like:** NewPart ≈ insert count, MergeParts a steady fraction, `error = 0` everywhere, TTL merges present on tables that declare TTL, merge results growing in size (levels climbing).
**Red flags:** MergeParts `error = 241` repeating on one partition (P-11), `error IN (40, 226, 1001, 49)` (P-31/P-30), NewPart thousands/hour with KB-sized parts (P-02), `DownloadPart` errors (P-44), no `TTLDeleteMerge` on TTL tables (P-18), `MutatePart` errors 53/70/117 (P-15/P-32), `system.*` tables with the largest merge memory (P-17).
**Traps:** sums are per **12-hour** bucket (1-hour with `hostname` on ≥ 23.11) — divide by `count`; `part_name` is deliberately absent; older bundles have the unaliased `any(exception)` column; gov drops exception columns and splits `table_name` into hashed `database`/`table`.
**Pairs with:** `system.parts`, `system.merges`, `system.mutations`, `asynchronous_insert_log`.

### system.metric_log_7_days
**Why we run it:** memory and background-pool saturation over time — the two counters that tell 'the server was overloaded' apart from 'one query misbehaved'; pool values pinned at the pool size (or > 256 in the raw metrics) mean the system is overloaded *(guideline)*.
**Question:** how did server memory and background pools behave hour by hour — the time series the dashboard does not draw.
**Read first:** `avg_memory_tracking_bytes` per hour vs RAM/cgroup/`max_server_memory_usage`; `max_merge_pool_tasks` vs `background_pool_size` (default 16); `avg_fetch_pool_tasks`; `zk_transactions` baseline and spikes; `zk_hw_exceptions`.
**Healthy looks like:** memory well under 80 % of the limit with diurnal shape, merge pool below its size most hours, `zk_hw_exceptions = 0`.
**Red flags:** memory > 80 % for consecutive hours (HC-5.1), a rising day-over-day floor (P-12), pool pinned at its size for hours (HC-2.7, P-01), any `zk_hw_exceptions` (HC-3.5, P-40), `zk_transactions` ×5 (Keeper load from many small inserts/mutations).
**Traps:** hourly **averages** hide short peaks; `avg_memory_tracking_bytes` is tracked allocations, not RSS (RSS is usually higher); requires `system.metric_log` enabled (it is by default).
**Pairs with:** `host_info.memory`, `query_log_details` (which hours were busy), `part_log.peak_memory_usage`.

### system.asynchronous_insert_log_7_days
**Why we run it:** async inserts fail silently when `wait_for_async_insert = 0`; the flush log is the only place a lost flush or a slow MV-bound flush shows up.
**Question:** if async inserts are used — are flushes succeeding, how big are they, how long do clients wait?
**Read first:** `status` totals (`Ok`, `ParsingError`, `FlushError`); `flushes` per hour per table; `total_rows/flushes` (batch size achieved); `avg_flush_ms`/`p90_flush_ms`.
**Healthy looks like:** all `Ok`, thousands of rows per flush, p90 flush under a second or two.
**Red flags:** `FlushError` (data lost if `wait_for_async_insert = 0`, P-33), `ParsingError` (client sends bad rows), `p90_flush_ms` in seconds (HC-7.2 — MV fan-out, P-34), tiny `total_rows/flushes` (buffer flushing on timeout with few rows).
**Traps:** absent on < 22.10 or when async inserts are unused (not a problem); `total_rows` only ≥ 23.4; hashed names in gov.
**Pairs with:** `query_log_details` `Insert` rows, `system.tables` (MVs on the target), `part_log` NewPart on the target.

### system.metric_log_coordination_3_days
**Why we run it:** `metric_log_7_days` keeps a fixed, version-stable handful of columns; this file takes every Keeper, object-storage, filesystem-cache and replication counter the version exports, hour by hour, so an outage of a *dependency* is visible even when no query failed.
**Question:** in which hours did Keeper stop answering, did S3 start returning errors, did the cache thrash, did fetches fail — and how do those hours line up with the workload?
**Read first:** `sum(ProfileEvent_ZooKeeperHardwareExceptions)` and `sum(ProfileEvent_ZooKeeperTransactions)` per hour (an outage = the first explodes while the second collapses); `max(CurrentMetric_ZooKeeperSession)` (0 in an hour = no session at all); `sum(ProfileEvent_S3ReadRequestsErrors)`, `…ReadBufferFromS3RequestsErrors`, `…S3WriteRequestsErrors`; `sum(ProfileEvent_ReplicatedPartFailedFetches)`; `max(CurrentMetric_ReadonlyReplica)`; `sum(ProfileEvent_FailedInsertQuery)`; `sum(ProfileEvent_RejectedInserts)` / `DelayedInserts`.
**Healthy looks like:** hardware exceptions 0 in every hour, transactions on a steady diurnal curve, S3 error columns 0 or tiny, `ReadonlyReplica` 0.
**Red flags:** any hour with hardware exceptions in the thousands or more (HC-3.8 Keeper health test — alerts `keeper_health` / `keeper_connection_blips`, P-57); `ReadonlyReplica` max > 0 (HC-1.4); S3 error columns rising in the hours before 107/`NoSuchKey` findings (HC-4.10, P-58); `ReplicatedPartFailedFetches` bursts (P-41).
**Traps:** column names carry the aggregate — `"sum(ProfileEvent_ZooKeeperTransactions)"` — and the set is **version-dependent** (a missing column means the version has no such counter, not that it was 0); values are per hour and, in cloud mode, summed/maxed over all replicas; a `CurrentMetric_*` hourly **max** hides sub-hour dips.
**Pairs with:** `metric_log_7_days` (memory and pools), `part_log_3_days` (merges stopped?), `query_log_details` (which codes in those hours), `zookeeper_connection`.

### system.text_log_histogram_1_day
**Why we run it:** the 2000-row `text_log` slice is the newest lines only; on a chatty server that is a few minutes. The histogram covers the whole day at hour × level × component granularity.
**Question:** when did Warning/Error volume change, in which component, and what did a typical line say?
**Read first:** Error and Fatal `count` per hour; the `logger_class` values that appear only in the incident hours; their `example`.
**Healthy looks like:** a flat, low Warning baseline from a few loggers, Error counts near zero, no Fatal.
**Red flags:** an hour where Error `count` is 10× the quiet hours (HC-11.5); classes typical of an incident: `ZooKeeperClient`, `DDLWorker`, `DatabaseReplicated`, `InterserverIOHTTPHandler`, `MergeTreeBackgroundExecutor`, `*::MetaInKeeper`/`DiskS3`, `executeQuery` with `Code: 107`; `BackgroundSchedulePool` "Temporarily pause scheduling" in the thousands (merges/fetches being throttled).
**Traps:** `logger_class` is a normalisation (table-specific loggers collapse to their component; plain loggers stay as they are); `example` is *one* line per cell, not the worst; gov keeps the class readable, masks a `<disk>::` prefix and hashes only unrecognised `db.table` fallbacks and the example.
**Pairs with:** `system.text_log` (the exact newest lines), `logs/*.err.log` (grep the hour), `query_log_details` (same hours).

### system.error_log_7_days (≥ 24.8)
**Why we run it:** `system.errors` is a cumulative counter since restart; `error_log` is its history — every code raised anywhere in the server, including background threads that never touch `query_log`, per hour.
**Question:** when did each error code start and stop; did 999/242/252/107 happen at night with no query to show for it?
**Read first:** `errors` per hour for 999, 242, 252, 107, 499, 241; `remote = 1` rows (errors received from other servers — interserver/distributed); the first hour a code appears.
**Healthy looks like:** a flat, low baseline of expected codes (60 UNKNOWN_TABLE from probes, 516 auth noise), no bursts.
**Red flags:** bursts that line up with the Keeper health test hours (HC-3.8); a code present in every hour at a steady rate (a retry loop — P-41/P-47); `remote = 1` bursts (peers failing, 86/221).
**Traps:** absent below 24.8 or when `<error_log>` is disabled; counts are events, not distinct incidents; the error *name* is the ClickHouse constant, not a message — pair with `system.errors.last_error_message` and `text_log`.
**Pairs with:** `system.errors` (message + trace), `query_log_details` (which queries), `part_log` (which merges), `metric_log_coordination`.

### system.text_log_keeper_1_day
**Why we run it:** the session-lifecycle lines (`Session expired`, `Finalizing session`, `Connected to ZooKeeper at <host>`, `Trying to establish a new connection`) are Information/Debug level, so the Warning-and-worse `text_log` slice never has them; they give the *minute* of a session loss and the Keeper host the server moved to.
**Question:** when exactly did this server lose and regain its Keeper session, how often, to which host?
**Read first:** `marker` counts per hour (`session_expired`, `session_finalized`, `reconnecting`, `connected`), `first_seen`/`last_seen`, the `connected` `example` (host:port).
**Healthy looks like:** no rows, or a single `connected` at server start.
**Red flags:** `session_expired`/`reconnecting` in the hours the Keeper health test (HC-3.8) flagged; repeated reconnects to *different* hosts (leader flapping); `connection_loss`/`operation_timeout` bursts.
**Traps:** one day only (`text_log` volume); a server with `text_log` level above Information will show nothing — say so; gov hashes the example.
**Pairs with:** `metric_log_7_days` / `metric_log_coordination` (the hours), `zookeeper_connection` (current session), `configuration/zookeeper.xml` (which hosts exist).

### system.zookeeper_log_errors_1_day (only when enabled)
**Why we run it:** `zookeeper_log` is the client-side record of every Keeper request and response — and the largest system table on a busy cluster (one row per request *and* per response; tens of GiB a day). Aggregating it whole is not affordable in a collector, so this file keeps only the **failed responses**: the part that says *which* operations failed with *which* Keeper error, and how many sessions were hit.
**Question:** which Keeper operations failed, when, with what error; how many sessions were affected?
**Read first:** per hour, `error` ∈ `ZSESSIONEXPIRED` / `ZCONNECTIONLOSS` / `ZOPERATIONTIMEOUT` (loss) vs `ZNONODE` / `ZNODEEXISTS` / `ZBADVERSION` (normal control flow); `op_num` of the failures (`Multi` = part commits, `Create`/`Set` = writes, `Get`/`List` = reads); `sessions_affected`; `max_duration_ms` of the failed calls.
**Healthy looks like:** only control-flow errors, at a steady rate; `sessions_affected` 1.
**Red flags:** loss errors in the hours the Keeper health test flagged (HC-3.10); `ZOPERATIONTIMEOUT` appearing *before* `ZSESSIONEXPIRED` (saturation first); `sessions_affected > 1` (the server went through several sessions in the hour).
**Traps:** absent file = `<zookeeper_log>` not configured, not health; successful volume and latency are deliberately **not** here — use `metric_log_coordination` (`ZooKeeperTransactions`, `ZooKeeperWaitMicroseconds / ZooKeeperTransactions` per hour) and `text_log_keeper_1_day` for session churn; even errors-only, the collector scans two 1-byte columns of the whole day, so on a huge cluster it can hit the collector timeout (159); durations are in ms on every version (converted from microseconds on ≥ 24.3).
**Pairs with:** `metric_log_coordination` (same hours from ProfileEvents), `text_log_keeper_1_day`, `zookeeper_connection`, `configuration/zookeeper.xml`.

### system.blob_storage_log_7_days (≥ 23.11, only when enabled)
**Why we run it:** object-storage operations leave no trace in `part_log`; this is where a deleted or never-uploaded blob can be seen.
**Question:** did uploads fail; when were blobs deleted; does a delete precede the reads that now fail?
**Read first:** `failed = 1` rows and their `example_error`; `Delete` counts per hour vs baseline; `Upload`/`MultiPartUpload*` bytes per hour (write throughput).
**Healthy looks like:** `failed = 0` everywhere, deletes a steady fraction of uploads (merges replacing parts).
**Red flags:** failed uploads (HC-4.9 — the part's data never reached the bucket; a later read fails with 107/`NoSuchKey`); a delete burst followed by 107 errors on the same tables (P-58); errors mentioning 403/credentials (a policy or key rotation problem, not data loss).
**Traps:** absent = table not configured; `bytes` excludes `Delete` rows (their `data_size` is a sentinel); the log names blobs by key, not by table — the owner maps a key to a part with `system.remote_data_paths`, which the bundle does not ship (too large).
**Pairs with:** `system.disks`/`storage_policies` (which disk), `query_log_details` 107 by table, `system.metrics` cache-object counts per replica.

### system.text_log (24 h slice, ≤ 2000 rows, severity first, ≤ 200 rows per logger)
**Why we run it:** the server's recent warnings and errors with full text — Keeper session loss, merge failures, retry loops and 'Too many parts' appear here with the object they concern.
**Question:** what did the server itself complain about most recently?
**Read first:** `min(event_time)` — if it is minutes before `max(event_time)`, the cap was hit and the slice is a snapshot, not a day (HC-11.4); then `logger_name` histogram; then the newest 20 Error/Fatal rows; then `Code: NNN` histogram from `message`.
**Healthy looks like:** a few Warnings per hour, no Error/Fatal, span covering the whole day.
**Red flags:** identical messages repeating (a retry loop — count, don't read), `Code: 999`/`Session expired` (P-40), `Code: 241` on merges (P-11), `broken`/`checksum` (P-31), `Too many parts` (P-01), `Not executing … because` (P-04), S3 `SlowDown` (P-45).
**Traps:** hashed `logger_name`/`message` in gov (only volume and timing survive); Debug/Trace excluded by design, so absence proves nothing; `text_log` is disabled by default in OSS — an absent file usually means that.
**Pairs with:** `system.errors`, `logs/`, and `--collect-text-log` for a bounded wider slice.

### text_log_<ts> (opt-in `--collect-text-log` slice)
**Why we run it:** when the default 24 h/2000-row slice is too narrow, a bounded slice at a chosen level captures the onset of an incident without dumping the whole table.
**Question:** everything the server logged at ≥ the chosen level inside an explicit window.
**Read first:** `count()` by `level`, by `logger_name`, by `message_format_string` (≥ 23.1 — collapses variants); the minute-by-minute count to find the onset; then the first 20 Error rows at the onset.
**Healthy looks like:** n/a — this is collected because something happened.
**Red flags:** whatever the incident is; look for the **first** error, not the loudest.
**Traps:** can be hundreds of thousands of rows — aggregate first; UTC timestamp in the filename.
**Pairs with:** `query_log_details` for the same window, `logs/` for lines below the chosen level.

## Other artefacts

### query_analysis/ (12 files, `--query-id` / `--normalized-query-hash`)
**Why we run it:** to explain one query's slowness you need its ProfileEvents against a fast run of the same shape, its pruning log lines and the DDL of the tables it touched — none of which the general collection carries (that has only hourly sums per hash).
**Question:** why is *this* query slow or failing — where does its wall time go, and how do slow runs differ from fast ones?
**Read first, in this order:**
1. `query_details` → `tables`, `is_initial_query`, `initial_user`, `query_kind`, `read_rows`, `read_bytes`, `query_duration_ms`, `memory_usage_human`, `peak_threads_usage`. `tables` starting with `_table_function.` (s3Cluster, s3, url, remote) means no MergeTree parts are involved; `is_initial_query = 0` means this hash is a **worker sub-query** — the tool picked the slowest *shard*, and the other "executions" are the other replicas' shares of the same statement (find the parent in `text_log_full`: `initial_query_id: …`).
2. `profile_events` → build the time budget: `query_duration_ms` (wall — `RealTimeMicroseconds` is summed across threads, do not use it as wall), `OSCPUVirtualTimeMicroseconds` (CPU used), `OSCPUWaitMicroseconds` (CPU wanted but not scheduled — throttling when ≈ CPU used), `NetworkSendElapsedMicroseconds` (rows shipped to the initiator/client), `ParquetFetchWaitTimeMicroseconds` / `ReadBufferFromS3Microseconds` / `S3ReadMicroseconds` (object storage waits), `DiskReadElapsedMicroseconds`, `SelectedMarks`/`SelectedParts`/`SelectedRows`, `MemoryAllocatedWithoutCheckBytes` (allocations the tracker did not see — Arrow/Parquet decode).
3. `executions_timeline` + `hash_by_host` → **runs or shards?** Same minute on different hosts with `read_rows` proportional to `query_duration_ms` = shards of one distributed statement (data skew); spread across hours on any host = independent runs (regression or contention).
4. Only then `profile_events_compare` (top `|delta|`; meaningful for runs, misleading for shards), `text_log_parts` ("Selected N/M parts … K marks" — pruning), `tables_for_query` (DDL, size), `text_log_full` (onset: `Reading object …` sizes, `Selected …`, the longest gap between consecutive lines = where the time went), `failed_over_time`/`failed_queries` (when and how it failed).
**Healthy looks like:** slow ≈ fast in `SelectedParts/Marks`; wall time explained by rows read; CPU wait ≪ CPU used; network send a minor share.
**Red flags:** `SelectedParts = total parts` (no pruning, P-50); huge `ReadBufferFromS3*`/`S3GetObject` deltas (cold cache / S3, P-45); `OSCPUWait ≈ OSCPUVirtualTime` (CPU-throttled pod, P-22); `NetworkSendElapsed` > 50 % of wall on a worker sub-query (initiator-side insert, P-56); memory deltas (P-52); one host ×10 p95 (P-44/P-54); a single `Reading object` several times larger than the others (file skew, P-56).
**Traps:** never in gov; `executions_timeline` ≤ 10 000 rows; full query text inside — privacy gate before sharing; **empty files are often "not applicable", not "not collected"**: `text_log_parts` and `tables_for_query` are empty for table-function queries, `failed_*` for queries that never failed — state the reason rather than flagging a gap; the auto-derived "fast" execution is just the smallest shard when the hash is a worker sub-query.
**Pairs with:** `query_log_details` (the hash's history and the parent statement's hash), `system.tables`, `metric_log` (was the server busy in that hour).

### host_info.json (onprem default; never gov)
**Why we run it:** THP, overcommit, cgroup limits, open-file limits and available memory explain a large share of self-managed incidents and are invisible from inside the database.
**Question:** what machine is this, is it the right one, and are the OS knobs ClickHouse cares about set sanely?
**Read first:** `os.hostname` vs hostnames seen elsewhere (is this the server or the operator's laptop?); `notes`; `cpu.logical_cpus` and `load_avg_1_5_15`; `memory.available_bytes`, swap used; `clickhouse_relevant_tunables` (THP, overcommit, `max_map_count`, `nofile`, cgroup limits); `disks[]` for the data mount; `top_processes_by_rss`.
**Healthy looks like:** THP `madvise`/`never`, overcommit `0`, `nofile` ≥ 500000, available memory ≫ 2 GiB, load < cores, no swap in use, ClickHouse the only large RSS.
**Red flags:** THP `always`, overcommit `2`, available < 2 GiB (HC-10.1–10.3 — the three ClickHouse warns about at startup), cgroup limit ≪ RAM (HC-10.4), low `nofile` (HC-10.5), load ≫ cores (HC-5.5), swap used (HC-5.6), a co-located process eating RAM (HC-5.7).
**Traps:** `available: false` + `notes` = degraded (usually collected off the server) — **ignore it and say so**; `disks[]` includes tmpfs/overlay; values are bytes, load averages are strings.
**Pairs with:** `metric_log` memory, `system.disks`, `logs/` startup warnings.

### logs/ (onprem default; never gov)
**Why we run it:** restarts, startup warnings, fatal stacks and the *first* error of an incident are only in the server log files; system tables lose them on restart.
**Question:** the server's own narrative — restarts, startup warnings, fatal errors, retry loops, the first error of an incident.
**Read first:** the `### support-diagnostic: TRUNCATED` header (is the beginning missing?); `grep -c '<Fatal>'`; `Starting ClickHouse`/`Ready for connections`/`Received termination signal` timestamps (restarts); `Code: NNN` histogram; `<Error>` logger histogram; the startup warning block (memory, THP, overcommit, `max_map_count`, TaskStats).
**Healthy looks like:** one start, no Fatal, few Errors, startup block with no warnings.
**Red flags:** repeated starts without termination (crashes/OOM-kills, HC-1.2, P-13/P-30), Fatal blocks (P-30), a message repeating hundreds of times per minute (retry loop — P-04/P-11/P-41), Keeper client errors (P-40), `broken`/`checksum` (P-31).
**Traps:** **grep, never read top-to-bottom**; `.err.log` is a subset of `.log`; the default 50 MiB cap keeps the *tail*; rotated files only with `-logs-include-archives`; timestamps are server-local.
**Pairs with:** `system.text_log` (structured view of the same), `system.crash_log`, `host_info`.

### configuration/ (onprem/cloud with access; never gov)
**Why we run it:** customised settings explain deviations from defaults — memory limits, pool sizes, Keeper topology, storage policies, log-table TTLs — and coupled settings that were changed on one side only.
**Question:** how is the server configured beyond defaults — memory limits, pools, Keeper, storage, logging, users' limits?
**Read first:** `config.d/*` and `users.d/*` names (what has been customised); memory (`max_server_memory_usage*`, `max_memory_usage`, caches), background pools, `<zookeeper>`/`<keeper_server>`, `<storage_configuration>`, `<remote_servers>`, `<macros>`, `<interserver_http_host>`, `<*_log>` sections (TTL?), `<listen_host>`, profile constraints/quotas.
**Healthy looks like:** few, deliberate overrides with comments; log tables with TTL; odd Keeper quorum; explicit interserver host; no `default` user without a password on an open `listen_host`.
**Red flags:** coupled settings overridden on one side only (P-16), pools raised without CPU, `parts_to_throw_insert` in the tens of thousands (P-01 insurance never reverted), no TTL on log tables (P-17), even/single Keeper nodes or duplicated macros (P-42), open listener + passwordless default (HC-10.10).
**Traps:** credentials are redacted but **hostnames, IPs, topology and identifiers are not** — never paste verbatim; unparsable files were skipped, so absence ≠ default; verify a setting exists on this version before commenting (`clickhouse-source.md` §2).
**Pairs with:** `host_info` (cgroup vs `max_server_memory_usage`), `system.clusters`, `logs/` startup (rejected config).

### dashboard.html (cloud/onprem)
**Keeper Health section (new):** the dashboard already applies the Keeper health test (HC-3.8) per hour from `DATA.keeper_metric_hourly` and shows the verdict table, the traffic-vs-exceptions chart (bars coloured red for *unavailable*, amber for *blip*), the mean latency line and the Keeper-dependent error codes per hour. Read its verdict first, then confirm against the JSONL files — the dashboard queried the server live at collection time, the files are the evidence you can quote.
**Why we run it:** carries the alert results *with matched rows* and a few pre-aggregated panels; it is the quickest headline and the only place fired-alert rows are recorded.
**Question:** the tool's own rendering of the same data, plus the **alert results with matched rows** and a few panels not in the JSONL (top tables list, per-user summaries, host checks).
**Read first:** extract the `const DATA = {…}` JSON (recipe in `reading-recipes.md` §1); `alerts[]` (`rows` present = fired, `error` = could not run, `skipped` = not applicable); `host_checks`; `version`/`uptime`; `query_slow`/`query_heavy`/`query_by_user` for a pre-aggregated view; `high_part_count`; `ttl_activity`.
**Healthy looks like:** no fired alerts, host checks all `ok`/`info`.
**Red flags:** any fired alert (HC-1) — but read the rule's code, not its name (`too_many_simultaneous_queries` filtered 252 in tool versions before September 2026); `uptime` short (restart).
**Traps:** the HTML loads Chart.js from a CDN — irrelevant for reading `DATA`; alert `rows` contain identifiers; withheld in gov.
**Pairs with:** `alerts/*.yaml` in the repo (thresholds and SQL of each rule).

### execution_log.txt (every mode)
**Why we run it:** the result files cannot say which collectors did not run, why, or what each cost; a missing file looks exactly like an empty table.
**Question:** did every collector run; which failed (grant, missing table, timeout); which were expensive on this server; how long did each phase take?
**Read first:** the *Summary* line; *Failed collectors* (error text — `Code: 60` unknown table = not enabled on this version/config, `Code: 497` = grant missing, `Code: 159` = the tool's own `-query-timeout` fired, `Code: 139` = no Keeper/DDL config); *Most expensive collectors*.
**Healthy looks like:** every collector `ok`, the slowest a few seconds, failures only for tables the server does not have.
**Red flags:** a collector with 159 (the server was too slow for the tool — itself a finding, HC-0); 497 on `system.*` (the bundle is grant-narrowed); `part_log`, `query_log` or `zookeeper_log` collectors taking minutes (huge log tables — propose a shorter `-from/-to` next time); a phase (dashboard, logs) dominating the run.
**Traps:** durations are wall time from the tool's host, including network; `rows` is `-1` for Native format; older bundles have no such file.
**Pairs with:** `HC-0` coverage, `system.errors` code 159/497 near collection time, `running-the-tool.md` §8 (re-collection).

### alerts_summary.json (gov, `-skip-dashboard`, or dashboard failure)
**Why we run it:** when no dashboard is produced it is the only proof the alert rules ran, and which fired, errored or did not apply.
**Question:** which alert rules ran, fired, errored or were not applicable — without any matched rows.
**Read first:** `fired`/`errored`/`not_applicable`/`evaluated`; per rule `state` and `instance_count`; `error`/`skip_reason` text.
**Healthy looks like:** `fired = 0`, `errored = 0`.
**Red flags:** fired rules by `severity`; `errored` rules are **not findings** (missing column/grant on this version) but mean the check did not happen — recompute it from the JSONL yourself (`health-checks.md`).
**Traps:** `evaluated` excludes skipped and errored; matched rows are never included in any mode.
**Pairs with:** the JSONL files behind each rule (`system.parts`, `system.disks`, `system.replicas`, `query_log_details`, `system.mutations`, `system.detached_parts`, `system.crash_log`).

---

## Scenario walkthroughs — why these files are read together

The entries above are per file. Real incidents cut across files; these walkthroughs show the order in which to open them, the number to compute, the threshold, and what crossing it means. Thresholds marked *(guideline)* come from support experience, the others from the tool's alerts or ClickHouse defaults. Live-server queries are shown for context; the **bundle** column says where the same signal lives in the archive.

### Replicated cluster under heavy load

Trigger: inserts slow or failing on a replicated setup, `TABLE_IS_READ_ONLY`/`KEEPER_EXCEPTION` in `system.errors`, or `absolute_delay` climbing.

| Step | Live query (for reference) | In the bundle | Threshold → meaning |
|---|---|---|---|
| 1. Is a replica falling behind, and how? | `SELECT type, count() FROM system.replication_queue GROUP BY type` | `system.replication_queue_*.jsonl` → `count()` by `type` | any single type **> 60** entries *(guideline)* → a replica is becoming unavailable; `GET_PART` dominant = fetch-bound (network/fetch pool), `MERGE_PARTS` dominant = merge-bound (CPU/merge pool). Read `postpone_reason` and `last_exception` for the specific blocker (P-41). |
| 2. Is the server itself overloaded? | `SELECT metric, value FROM system.metrics WHERE metric LIKE '%Pool%'` | `system.metric_log_7_days_*.jsonl` → `max_merge_pool_tasks` (= `BackgroundMergesAndMutationsPoolTask`), `avg_fetch_pool_tasks` (= `BackgroundFetchesPoolTask`) per hour | any pool value **> 256**, or the merge pool pinned at `background_pool_size` for consecutive hours → system overloaded (HC-2.7, P-01). `BackgroundSchedulePoolTask` and the other pools are **not** in the bundle — ask for `system.metrics` if needed. |
| 3. Has Keeper let go of the replica? | `SELECT database, table, replica_name FROM clusterAllReplicas(default, system.replicas) WHERE is_readonly` | `system.replicas_*.jsonl` → `is_readonly`, `is_session_expired`; `system.errors` 999/242; `metric_log.zk_hw_exceptions` | any `is_readonly = 1` → writes to that table are refused until the session is re-established; `is_session_expired = 1` or `zk_hw_exceptions > 0` in the same hours → Keeper timeouts/session expiry are the cause (P-40), not the table. |
| 4. What is the load made of? | — | `system.part_log_3_days` NewPart per hour and `size_in_bytes/count`; `query_log_details` Insert `count` and `written_rows/count` | thousands of tiny inserts per hour → the Keeper transaction rate and part count are self-inflicted (P-02); otherwise look at a merge/mutation blocking the pool (P-04, P-11). |

Reading: step 1 tells you *which side* is behind, step 2 whether capacity is exhausted, step 3 whether coordination broke, step 4 what to change. Present all four numbers together — a high queue with an idle pool and no Keeper errors is a blocked merge, not an overloaded server.

### Inserts rejected with TOO_MANY_PARTS (252)

1. `system.parts` (active) → parts per `(database, table, partition_id)`; **> 300** = alert, ≥ `parts_to_throw_insert` (OSS default 3000 on ≥ 23.6) = rejections (HC-2.1).
2. Same file → `countIf(level = 0)` and `avg(rows)`; hundreds of level-0 parts with avg rows **< 10 000** → inserts too small (P-02).
3. `system.tables` → `partition_key` of that table; per-hour/per-tenant keys → fan-out (P-03).
4. `system.part_log_3_days` → `NewPart` vs `MergeParts` per hour, and `MergeParts` with `error != 0` (241 = merge OOM loop, P-11).
5. `system.merges` → a merge with `elapsed` in hours and flat `progress` occupying a slot (P-04).
6. `system.mutations` → hundreds of `DELETE`/`UPDATE` commands on that table (P-05).

### Queries failing with MEMORY_LIMIT_EXCEEDED (241)

1. `system.query_log_details_7_days` → `exception` text class: *(for query)* / *(total)* / *(for user)* / *while pushing to view* / *AggregatingTransform* / *FillingRightJoinSide* (P-10).
2. `system.metric_log_7_days` → `avg_memory_tracking_bytes` in the failing hours vs RAM (`host_info.memory.total_bytes`, cgroup) → server at ceiling (P-12) or one query (P-52).
3. `system.part_log_3_days` → `peak_memory_usage` of merges in the same hours; `system.*` tables on top → P-17.
4. `configuration/` → `max_server_memory_usage*`, `max_memory_usage`, cache sizes; `host_info.clickhouse_relevant_tunables` → THP/overcommit (P-13).

### Disk filling up

1. `system.disks` → `free_pct` **< 15** critical, **< 5** inserts fail (HC-4.1).
2. `system.parts` (active) → `sum(bytes_on_disk)` by `database`; `system` in the top 3 → log tables without TTL (P-17).
3. `system.detached_parts` → `sum(bytes_on_disk)`; `logs/` sizes; `host_info.disks` for the log volume.
4. `system.tables` DDL with `TTL` but no TTL events in `part_log` → TTL not applied (P-18).

### Slow or regressed after an upgrade

1. `system.version` + `logs/` start banners → when the version changed; mixed versions in `system.clusters`/banners (P-31).
2. `system.query_log_details_7_days` → `query_duration_ms/count` and `memory_usage/count` per hash before vs after the change hour (P-54).
3. `system.part_log_3_days` → merge duration/size per hour before vs after.
4. `query_analysis/profile_events_compare` if collected across both periods; otherwise propose `--normalized-query-hash` for the worst hash.
5. `clickhouse-source.md` → release notes between the two builds.

### Server crashed or restarted

1. `system.crash_log` → rows with `signal`, `version`, `query_id`, top frames (P-30); empty file + restart in `logs/` → OOM-kill by the OS (P-13).
2. `logs/clickhouse-server.log` → `Starting ClickHouse`/`Received termination signal` timeline; `<Fatal>` blocks; the startup warning block.
3. `host_info.json` → `memory.available_bytes`, cgroup limit, THP, overcommit.
4. `system.query_log_details_7_days` → the hash of `crash_log.query_id`'s pattern; `system.errors` 49/1001 counts.

### Keeper outage on a cluster whose tables live on object storage

1. `system.metric_log_coordination_3_days` → the hours where `sum(ProfileEvent_ZooKeeperHardwareExceptions)` explodes and `…ZooKeeperTransactions` collapses (HC-3.8). Those hours are the incident; everything else is before or after.
2. `system.part_log_3_days` → `MergeParts` with `error = 999` in those hours, then hours with `NewPart > 0` and `MergeParts = 0` (HC-2.12): merges stopped. `system.query_log_details_7_days` → 999/319 on inserts, 571 and 57 (`.tmp.inner_id` UUID collisions) on DDL, then 252 on the busiest insert target (P-57).
3. `system.zookeeper_connection` → session age since recovery; `system.zookeeper_log_errors_1_day` (if present) → sessions per hour and `ZSESSIONEXPIRED`; `configuration/zookeeper.xml` → ensemble size; `system.databases` → `Replicated` count (load multiplier).
4. `system.distributed_ddl_queue` → entries not `Finished`, the replayed `CREATE OR REPLACE` statements (HC-3.11). `system.errors` → 221/86 counts vs `Uptime` (recovery noise or persisting).
5. **After recovery:** `query_log_details` → 107 `FILE_DOESNT_EXIST` / `The specified key does not exist` on `Active` parts by table; `system.disks.type = ObjectStorage` + `storage_policies` confirm object storage; `system.metrics` per replica → `MetadataFromKeeperCacheObjects` skew; `blob_storage_log_7_days` → deletes/failed uploads for the hour (P-58). Recommend metadata refresh (`SYSTEM RESTART REPLICA`, `SYSTEM DROP DISK METADATA CACHE`, rolling restart) before anything that touches data.
6. **Coverage caveat:** on a SharedMergeTree cluster an `onprem` bundle is one replica of N (HC-0) — say which, and propose `-mode cloud` for the cluster view. Keeper's own logs and `mntr` output are not in the bundle; ask for them.
