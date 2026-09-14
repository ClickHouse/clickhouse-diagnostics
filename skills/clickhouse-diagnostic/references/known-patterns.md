# Known patterns — recurring ClickHouse problems as they look inside a bundle

Distilled from ClickHouse support experience (public documentation, ClickHouse engineering guidance and an anonymised sample of ~100 closed support escalations, 2023–2026). Every entry is written from the point of view of **what you can see in the bundle**; nothing here identifies a customer, a service or a table. Cloud-only mechanics (pods, operators, Keeper autoscaling) are mentioned only where they change what an OSS/on-prem reader should conclude.

Language rule when you match a pattern: "the evidence is **consistent with** P-nn" / "this **resembles** …" — never "this is a known issue" or "you are hitting bug #N" (see `clickhouse-source.md` §4). A pattern match is a hypothesis to verify with the second source listed under *Verify*.

## Quick reference (scan this, then open only the entries that match)

| Id | Symptom-first label | Bundle files involved |
|---|---|---|
| P-01 | Part count explodes — inserts outrun merges | parts, part_log, merges, metric_log |
| P-02 | Many tiny inserts (rows per part in the hundreds) | parts, part_log NewPart, query_log Insert |
| P-03 | Partition key too fine / thousands of partitions | tables.partition_key, parts |
| P-04 | Backlog grows while the merge pool looks idle | merges, part_log, metric_log, tables.engine_full |
| P-05 | Row-level DELETE/UPDATE storms create a mutation backlog | mutations, part_log MutatePart |
| P-10 | MEMORY_LIMIT_EXCEEDED — reading the message class | query_log exception, errors |
| P-11 | A merge/mutation OOMs in a loop and pins its parts | part_log 241/236, merges, mutations |
| P-12 | Memory floor rising over days (leak vs traffic vs caches) | metric_log, query_log, host_info |
| P-13 | OS-level exhaustion: 173, "Too many open files", THP, cgroup | errors, host_info, logs |
| P-14 | Mutation "stuck": killed ≠ finished, head-of-line behind a merge | mutations, merges, part_log |
| P-15 | TOO_MANY_MUTATIONS / one failing mutation retried forever | mutations, part_log 53/70/117/349 |
| P-16 | Invalid setting combination at startup (Code 36) | logs startup, configuration |
| P-17 | System log tables eat disk, merge memory and CPU | parts (system db), part_log, configuration |
| P-18 | TTL not applied / expired data not removed | parts delete_ttl_info, part_log TTL merges, tables |
| P-20 | TOO_MANY_SIMULTANEOUS_QUERIES — pile-up, not traffic | query_log 202, processes, metric_log |
| P-21 | Timeouts: server limits, client cancels, distributed DDL | query_log 159/394, text_log |
| P-22 | Thread exhaustion symptoms (439/460/1000 "No thread available") | errors, text_log, metric_log |
| P-30 | Crash or LOGICAL_ERROR repeating on one version | crash_log, logs Fatal, version |
| P-31 | Broken/unreadable parts: corruption or version skew | detached_parts, errors 40/226/33, logs |
| P-32 | Schema drift between MV/source/target, failing type conversions | tables DDL, query_log 53/70/117/349, part_log |
| P-33 | Inserts "succeed" but rows are missing, duplicated or wrong | query_log Insert, text_log dedup, async_insert_log, tables (MV) |
| P-34 | MV chains amplify every insert (and can block async flushes) | tables MV count, part_log NewPart per target, async_insert_log |
| P-40 | Keeper session loss → replicas read-only, 999 bursts | replicas, errors 999/242, metric_log zk_*, text_log |
| P-41 | Replication queue stuck / large absolute_delay | replication_queue, replicas, configuration |
| P-42 | Topology/config mistakes: interserver host, macros, quorum, new node | configuration, clusters, replicas |
| P-43 | Stale Keeper metadata: ghost replicas, orphan UUIDs, old block numbers | replicas total vs active, errors 253/244/308, logs |
| P-44 | Network/DNS/interserver failures | clusters.errors_count, errors 198/209/210/279, part_log DownloadPart |
| P-45 | Object storage (S3) throttling, latency, moves and disk tiering | errors 499, text_log, disks, part_log |
| P-50 | ORDER BY / partitioning design that defeats pruning | tables sorting/partition_key, query_analysis text_log_parts |
| P-51 | FINAL on Replacing/Collapsing tables — full scans | query_log query sample, tables engine |
| P-52 | GROUP BY / JOIN memory and the join build side | query_log 241 message, query sample |
| P-53 | Indexes not used: skip-index/LIKE mismatch, analyzer behaviour changes | tables indices in DDL, query sample, version |
| P-54 | Regression after an upgrade (perf, memory, CPU, behaviour) | version, query_log by hour, part_log, logs |
| P-55 | Monitoring/internal tooling dominates the query load | query_log by user, tables count |
| P-56 | Distributed `INSERT … SELECT` / `s3Cluster` load is slow: worker shards, file skew, initiator-side insert | query_analysis (query_details, profile_events, executions_timeline, text_log_full), part_log |
| P-57 | Keeper quorum loss / leader flapping → session-expiry cascade: 999 and 319 on inserts, merges stop (252 follows), Replicated-database DDL replay (571, 57 UUID collisions), interserver 221/86 storms on recovery | metric_log_coordination, zookeeper_connection, zookeeper_log, part_log errors, query_log by hour, distributed_ddl_queue, databases |
| P-58 | After a Keeper or object-storage incident, reads fail with 107 FILE_DOESNT_EXIST / `The specified key does not exist` on parts that are `Active` — stale part metadata (local or Keeper-held cache) | query_log 107 by table, system.metrics `MetadataFromKeeperCacheObjects` per replica, blob_storage_log, disks/storage_policies, part_log 84/504 |

---

## Parts and merges

### P-01 Part count explodes — inserts outrun merges
**You see:** `system.parts` (active) with hundreds–thousands of parts in one partition, many at `level = 0`; `part_log` shows `NewPart` events far outnumbering `MergeParts` per hour; `metric_log.max_merge_pool_tasks` pinned at the pool size; `query_log_details` has 252 `TOO_MANY_PARTS` on inserts; sometimes CPU throttling in the same hours.
**Usually means:** a feedback loop — more parts make merge selection slower, so fewer merges get scheduled, so parts grow. It is not self-correcting. Triggers: an insert-rate step change, a burst of row-level deletes/updates (P-05), a merge-pool saturation from another table, a background hiccup (Keeper, S3) that paused merges for minutes.
**Verify:** hourly `NewPart` count vs `MergeParts` count for the table; `avg(rows)` of level-0 parts (small = P-02); `system.merges` for long-running/stuck merges (P-04, P-11).
**Fix / mitigation:** stop or throttle inserts to let merges catch up; write larger batches (10k–100k rows) or enable `async_insert`; temporarily raise `min_parts_to_merge_at_once` (≈50) then **reset** it; raise `parts_to_throw_insert`/`parts_to_delay_insert` only as short-term insurance and restore defaults afterwards (OSS defaults 3000/1000 on ≥ 23.6; older 300/150); `background_pool_size` up only if CPU is free. `OPTIMIZE TABLE` rarely helps during an explosion — it competes with background merges.
**Not to be confused with:** P-03 (many partitions, each with few parts) and P-04 (pool idle but selector not scheduling).
**Public references:** docs → MergeTree settings (`parts_to_throw_insert`, `parts_to_delay_insert`, `max_parts_in_total`), "Asynchronous inserts".

### P-02 Many tiny inserts
**You see:** `part_log` `NewPart` with `size_in_bytes/count` in KBs and thousands of events per hour on one table; `query_log_details` `Insert` count/hour in the thousands with `written_rows/count` < 1000; active parts with `rows` in the hundreds.
**Usually means:** the client sends one small INSERT per event/row (or a connector with a tiny batch size). Each insert = one part per partition per MV target.
**Verify:** `query_log_details` by `user`/`interface` for the insert pattern; `system.tables` for MVs on the table (each multiplies part creation).
**Fix:** batch on the client (~100k rows or 1–5 s), or server-side `async_insert = 1` with `wait_for_async_insert = 1` (keep it 1 unless silent loss is acceptable). Connectors: raise batch size/timeout rather than lowering them. For `INSERT … SELECT` migrations use large `min_insert_block_size_rows/bytes`.
**Public references:** docs → "Asynchronous inserts", "Bulk inserts" best practices.

### P-03 Partition key too fine / too many partitions
**You see:** `system.tables.partition_key` like `toStartOfHour(ts)`, `(toDate(ts), tenant)`, `xxh3(...) % N`; `system.parts` with thousands of `partition_id` values per table, each with a handful of small parts; TOO_MANY_PARTS messages mentioning "in all partitions in total" (`max_parts_in_total`, default 100000) or "This indicates wrong choice of partition key".
**Usually means:** partitioning is being used as an index. Merges never cross partitions, so old, small partitions never consolidate, and an insert touching N partitions creates N parts. Multi-tier retention keys (`(date, retention_bucket)`) accumulate many small parts in long-retention partitions.
**Verify:** parts per partition distribution; `avg_parts_per_partition` for old partitions; insert shape (does one insert span many partitions?).
**Fix:** partition by month/day at most (one partition should hold GBs), keep tenant/product out of the key; for existing long-retention partitions run a periodic `OPTIMIZE TABLE … PARTITION ID '…' FINAL`; stage heavily partitioned inserts through a `Null` table + MV. Raising `max_parts_in_total` only defers the problem.
**Public references:** docs → "Custom partitioning key" ("Partitioning is not intended to speed up SELECT queries").

### P-04 Backlog grows while the merge pool looks idle
**You see:** high part counts (P-01) but `system.merges` shows 0–2 merges, `metric_log.avg_merge_pool_tasks` far below the pool size, CPU idle; `part_log` shows few `MergeParts` per hour; sometimes `text_log` repeats "Not executing … because N merges already executing", "Another merge assigned for some of our source parts", or a merge-selecting task backing off.
**Usually means:** one of: (a) a single huge or endlessly retrying merge occupies a slot (S3 errors, OOM loop → P-11) and blocks the merge predicate for that partition; (b) `min_age_to_force_merge_seconds` (or `_on_partition_only`) forced a partition-wide merge too large to finish; (c) with ~30k+ parts each selection scan (hundreds of ms) yields one merge — selection, not execution, is the bottleneck; (d) intermediate levels (L2–L4) stall against the ~150 GB result-size ceiling; (e) on Cloud/SharedMergeTree, the merge coordinator's prepare-count cap or a mismatched `background_pool_size` across replicas.
**Verify:** `system.merges` rows with `elapsed` in hours and `progress` flat; `engine_full` for `min_age_to_force_merge_seconds`, `max_parts_to_merge_at_once`, `max_bytes_to_merge_at_max_space_in_pool`; `part_log` errors on the same partition.
**Fix:** unblock the occupant (kill/scale for the OOM merge, fix S3, set `min_age_to_force_merge_seconds = 0` temporarily); lower `max_parts_to_merge_at_once` to spread work if huge merges OOM; batch inserts to cut the selection set; consider `max_parts_to_merge_at_once` up and `parts_to_throw_insert` insurance during recovery, then revert.
**Public references:** docs → MergeTree settings (`max_bytes_to_merge_at_max_space_in_pool`, `min_age_to_force_merge_seconds`, `max_parts_to_merge_at_once`).

### P-05 Row-level DELETE/UPDATE storms
**You see:** `system.mutations` with hundreds of `UPDATE _row_exists = 0 WHERE …`/`DELETE WHERE …` commands on one table; `part_log` `MutatePart` events dominating; part counts rising (P-01) and `mergeSelectingTask`/mutation CPU high; `MEMORY_LIMIT_EXCEEDED` on mutations.
**Usually means:** the application issues many small lightweight deletes/updates; every part must be rewritten (or masked) per mutation, and mutations serialize per part. Each pending mutation also keeps parsed commands in memory.
**Verify:** count mutations per table and per hour of `create_time`; `parts_to_do` vs total parts; `max_number_of_mutations_for_replica`/`number_of_mutations_to_throw` in `engine_full`.
**Fix:** batch deletes into fewer statements; prefer `DROP PARTITION`/`REPLACE PARTITION` for time- or tenant-bounded removal; model soft deletes (ReplacingMergeTree `is_deleted`, `argMax`) instead of mutating; cap concurrency with `max_number_of_mutations_for_replica`; `lightweight_deletes_sync = 0` to avoid client timeouts (the delete still applies in background).
**Public references:** docs → "Lightweight DELETE", "ALTER TABLE … DELETE", "Manipulating partitions".

## Memory, mutations, TTL, configuration

### P-10 MEMORY_LIMIT_EXCEEDED — read the message class first
**You see:** `exception_code = 241` in `query_log_details`/`system.errors`; the `exception` text tells you which limit and where:
- `Memory limit (for query) exceeded` → `max_memory_usage` (per query).
- `(total) memory limit exceeded … current RSS: X, maximum: Y` → server limit (`max_server_memory_usage` / `_to_ram_ratio`, default 0.9 of RAM or cgroup). If RSS ≈ maximum with "attempt to allocate chunk of 0.00 B", the whole server is at the ceiling; the failing query is a victim, not the cause — find what holds the memory (merges, other queries, caches, P-12).
- `Memory limit (for user) exceeded` → `max_memory_usage_for_user`; concurrent queries of one user share it. "would use 350 GiB … maximum 186 GiB" can also be tracker accounting under async-insert/MV fan-out (P-34).
- `While executing AggregatingTransform` → GROUP BY state (P-52). `FillingRightJoinSide` → JOIN build side (P-52). `while pushing to view` → MV chain (P-34). `While executing MergeTreeSequentialSource` / `(while reading column X)` in `part_log` → a merge (P-11). `ArrowBlockInputFormat`/`Parquet…` → input parsing of a large batch.
- `OvercommitTracker decision: Query was selected to stop` → the overcommit tracker picked this query when the total was exhausted; `memory_overcommit_ratio_denominator = 0` protects a query from selection but does not guarantee success.
**Verify:** hour of the failure vs `metric_log.avg_memory_tracking_bytes`; `part_log.peak_memory_usage` in the same hour; concurrent query count.
**Fix:** depends on class — see P-11, P-12, P-52; raise limits only after the consumer is identified. On Cloud, "service too small for the inserts" is a real answer (scale up the minimum).
**Public references:** docs → "Memory limit exceeded" KB, `max_memory_usage`, `max_server_memory_usage_to_ram_ratio`, "Memory overcommit".

### P-11 A merge or mutation OOMs in a loop and pins its parts
**You see:** `part_log` for one table/partition alternating `MergeParts error 241 (… while reading column X)` and `error 236 Cancelled merging parts (ABORTED)` for days; `system.merges` shows the same result part with low `progress` and high `elapsed`; `system.mutations` with `parts_to_do_names` pointing at parts of that partition, `parts_to_do` flat; part count in that partition growing.
**Usually means:** the merge needs more memory than the server can give (a column with huge values, JSON/Object columns with many dynamic paths, wide aggregate states, a partition with a 19+ GB part). The merge predicate excludes parts assigned to a running/retrying merge, so mutations behind it wait forever and look "stuck".
**Verify:** `part_log.peak_memory_usage` of the failing merge vs limits; the column named in the message; `system.tables` DDL for `JSON`/`Object`/`Map` columns; `merges.memory_usage`.
**Fix:** temporarily scale memory (or free it — drop mark cache, pause other work) so the merge completes once, then mutations drain by themselves; lower `max_parts_to_merge_at_once`; for JSON columns on affected versions consider `object_shared_data_serialization_version = 'map_with_buckets'` (metadata-only, applies to future merges) or fewer `max_dynamic_paths`; for MV/projection-heavy inserts lower `min_insert_block_size_bytes`. As a last resort detach the partition (RMT: `DETACH PARTITION` cancels assigned merges), re-attach healthy parts.
**Not to be confused with:** P-14 (healthy head-of-line blocking, 0 errors) and P-30 (merge failing with 1001/49 — a bug, not memory).
**Public references:** docs → `max_parts_to_merge_at_once`, JSON data type (`max_dynamic_paths`, shared data serialization).

### P-12 Memory floor rising over days
**You see:** `metric_log.avg_memory_tracking_bytes` (or host RSS) stepping up day by day until a restart; 241 on small queries; caches and tracked components well below RSS.
**Usually means (in order of frequency):** (1) **traffic growth** — more concurrent queries/inserts drive higher peaks that the allocator retains as its watermark: per-query `memory_usage/count` in `query_log_details` is flat while hourly `count` grew; (2) **retained metadata** — very many parts (primary key + marks in memory: sum `primary_key_bytes_in_memory_allocated` in `system.parts`), thousands of mutations kept (`finished_mutations_to_keep`), huge dictionaries (`system.dictionaries.bytes_allocated`), a large mark/uncompressed/filesystem-cache metadata; (3) **a version-specific regression** — floor rises only after an upgrade (P-54); (4) genuine leak — only after (1)–(3) are excluded, and it needs an allocator profile, not a bundle.
**Verify:** hourly `count` and `memory_usage/count` trends; `sum(primary_key_bytes_in_memory_allocated)` and part count; `mark_cache_size`/`uncompressed_cache_size` in `configuration/`; version change dates in `logs/`.
**Fix:** name the traffic vector (user/table/hash) if (1); reduce part count / dictionaries / caches if (2); upgrade or roll back if (3). Do not recommend allocator tweaks without measurements.
**Public references:** docs → "Memory overcommit", server settings `mark_cache_size`, `uncompressed_cache_size`, `max_server_memory_usage_to_ram_ratio`.

### P-13 OS-level exhaustion
**You see:** `system.errors` 173 `CANNOT_ALLOCATE_MEMORY`, 76/107 with "Too many open files"; `host_info.clickhouse_relevant_tunables`: `transparent_hugepages = always`, `vm_overcommit_memory = 2`, `clickhouse_open_files_soft` small, `vm_max_map_count` 65530; `cgroup_memory_limit_bytes` far below `memory.total_bytes`; `logs/` startup warnings about the same.
**Usually means:** the host, not ClickHouse's tracker, refused resources. In containers the working-set (RSS + page cache) is what the OOM killer counts, so a service can die while `metric_log` memory looks fine.
**Verify:** startup banner warnings in `logs/clickhouse-server.log`; restarts without `crash_log` rows (OOM-kill leaves none); `host_info.memory.available_bytes`.
**Fix:** THP `madvise`/`never`; `vm.overcommit_memory = 0`; `LimitNOFILE` ≥ 500000; `vm.max_map_count` ≥ 262144; size `max_server_memory_usage_to_ram_ratio` against the cgroup limit; keep swap off.
**Public references:** docs → "Usage recommendations" (THP, overcommit), "Requirements".

### P-14 Mutation "stuck": killed ≠ finished, head-of-line behind a merge
**You see:** `system.mutations` rows older than hours/days with `parts_to_do > 0`, `latest_fail_reason` empty (cloud bundles: `is_killed = 1` but still listed); `ALTER`/`DROP INDEX`/`DROP PROJECTION` failing with 517 `CANNOT_ASSIGN_ALTER` or "affected by mutation"; `part_log` shows a long `MergeParts`/`MutatePart` on the parts named in `parts_to_do_names`; 384 `PART_IS_TEMPORARILY_LOCKED` on part operations.
**Usually means:** mutations apply per part in submission order and skip parts currently assigned to a merge/mutation. A giant part (tens of GB, unpartitioned table) or a looping merge (P-11) blocks the chain. `KILL MUTATION` only sets the killed flag — the entry stays until each remaining part is processed (with a no-rewrite shortcut) — so "killed but not done" is normal for a while.
**Verify:** intersect `parts_to_do_names` with `system.merges.result_part_name`/source parts and with `part_log` failures; `parts_to_do` trend if two bundles exist.
**Fix:** let it drain once the blocker finishes; do not kill more mutations; for RMT, `DETACH PARTITION` cancels assigned merges (re-attach healthy parts); `SYSTEM RESTART REPLICA db.table` resets in-memory merge assignments when a killed mutation wedges on locked parts; long-term, partition the table so mutations spread over bounded parts, and avoid heavy `MODIFY COLUMN` concurrently with delete workloads.
**Public references:** docs → "Mutations", `KILL MUTATION`, `SYSTEM RESTART REPLICA`.

### P-15 TOO_MANY_MUTATIONS / one failing mutation retried forever
**You see:** hundreds–1000 rows in `system.mutations` for one table (692 `TOO_MANY_MUTATIONS` at `number_of_mutations_to_throw`, default 1000); `part_log` `MutatePart` with `error IN (53, 70, 117, 349)` and an `exception` like "Cannot convert NULL value to non-Nullable type", "Cannot parse … as Int64", "Incorrect data"; new inserts may fail with the same code.
**Usually means:** a schema change that the existing data cannot satisfy (`MODIFY COLUMN col String` with NULLs; a JSON path type hint contradicted by real values; an aggregate-state type change) fails on the first part and is retried indefinitely; the app keeps adding mutations behind it.
**Verify:** `command` of the oldest unfinished mutation; the failing part's data type in `system.columns`.
**Fix:** `KILL MUTATION` the failing one, fix the type (`Nullable(...)`, correct hint) or the source data, re-issue; raising `number_of_mutations_to_throw` masks the cause.
**Public references:** docs → `number_of_mutations_to_throw`, `ALTER TABLE MODIFY COLUMN`.

### P-16 Invalid setting combination at startup
**You see:** `logs/clickhouse-server.err.log` at startup: `Code: 36 … 'number_of_free_entries_in_pool_to_execute_mutation' (N) … is greater than … 'background_pool_size'*'background_merges_mutations_concurrency_ratio' … mutations cannot work with these settings`, or 139/318 config errors; `configuration/` overriding one side of a coupled pair; mutations never scheduled; DDL stalls.
**Usually means:** MergeTree pool-reservation settings must stay below `background_pool_size × background_merges_mutations_concurrency_ratio` (ratio default 2). Changing the pool size (or a platform auto-scaling it) invalidates a fixed override. A high `free_entries` value *throttles* mutations to protect merges — raising the pool is what makes it legal again.
**Verify:** grep `configuration/` for both settings; `system.tables.engine_full` per-table overrides (take the highest).
**Fix:** pin `background_pool_size` explicitly whenever `number_of_free_entries_in_pool_to_execute_mutation`/`_optimize_entire_partition` is overridden; verify the effective pool via `metric_log.max_merge_pool_tasks`.
**Public references:** docs → server settings `background_pool_size`, `background_merges_mutations_concurrency_ratio`; MergeTree `number_of_free_entries_in_pool_to_execute_mutation`.

### P-17 System log tables eat disk, merge memory and CPU
**You see:** `system.parts` grouped by `database` puts `system` among the largest; `part_log` shows the highest `peak_memory_usage` merges on `system.metric_log`/`system.text_log`/`system.trace_log` (tens of GiB) while user tables merge in MBs; 241 on merges of `system.*`; `text_log` at `trace` level; no `<ttl>` in `configuration/` `<text_log>`/`<query_log>` sections; sudden part explosions coinciding with a burst of system-table inserts.
**Usually means:** log tables grow unbounded (default: no TTL) and their wide compact parts are expensive to merge; `metric_log` in particular has hundreds of columns.
**Verify:** `system.tables` engine/TTL for `system.*_log`; `configuration/` log sections; `logs/` level.
**Fix:** add `<ttl>event_date + INTERVAL 30 DAY DELETE</ttl>` (or `<engine>` with TTL) to each `<*_log>` section; set `text_log` level to `information` or higher in production; consider `min_bytes_for_wide_part = 0` on `metric_log` when merges OOM; disable logs you never read (`trace_log`, `query_thread_log`, `part_log` at very high insert rates only if you have external observability).
**Public references:** docs → "System log tables" (`query_log`, `text_log`, `metric_log` config: `ttl`, `partition_by`, `flush_interval_milliseconds`).

### P-18 TTL not applied / expired data not removed
**You see:** `system.tables.create_table_query` has a `TTL`, but `system.parts` for old partitions still has rows past the TTL; `delete_ttl_info_min/max` = `1970-01-01 …` on recently written parts; no `merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')` or `RemovePart` events in `part_log` over 7 days; disk usage climbing (HC-4).
**Usually means:** (a) TTL merges are rate-limited (`merge_with_ttl_timeout` default 4 h per table, `max_number_of_merges_with_ttl_in_pool` default 2) and the backlog never clears; (b) a `MODIFY TTL` was interrupted/killed and new parts are written without TTL info — the table needs `MATERIALIZE TTL`; (c) `materialize_ttl_after_modify = 0` was used and step 3 (materialize) never ran; (d) TTL expression uses non-deterministic functions (`now()`), re-evaluated at every merge; (e) `ttl_only_drop_parts = 0` forces row rewrites on huge parts that never get scheduled.
**Verify:** `delete_ttl_info_*` on a recently created part vs an old one; `engine_full` for TTL settings; `part_log` TTL events.
**Fix:** table-level `ttl_only_drop_parts = 1`, `merge_with_ttl_timeout` 600–1800, `max_number_of_merges_with_ttl_in_pool` 8–16 (revert after backlog); `ALTER TABLE … MATERIALIZE TTL` off-peak (with `materialize_ttl_recalculate_only = 1` first on large tables); use column-based TTL expressions.
**Public references:** docs → "TTL for columns and tables", MergeTree settings `ttl_only_drop_parts`, `merge_with_ttl_timeout`.

## Concurrency and timeouts

### P-20 TOO_MANY_SIMULTANEOUS_QUERIES is usually a pile-up
**You see:** 202 in `query_log_details`/`errors` ("Too many simultaneous queries. Maximum: 100/1000"); hourly `count` of *finished* queries not higher than usual, but `query_duration_ms/count` up ×10; `system.processes` (at collection) full of long `elapsed`; often 159/241/999 in the same hours.
**Usually means:** something made queries slow (Keeper trouble P-40, a merge/CPU/IO saturation, an upgrade regression P-54, a thread cap), so arrivals outran completions and the admission limit tripped. Two specific self-inflicted variants: a workload/scheduler thread cap (`max_concurrent_threads_ratio_to_cores`, `concurrent_threads_soft_limit_*`) admitting up to `max_concurrent_queries` while only a few dozen threads execute — queries queue for minutes; and switching many insert streams to `async_insert` with `wait_for_async_insert = 1` on tables with dozens of MVs, so synchronized flushes execute thousands of MV sub-queries at once.
**Verify:** `query_log_details` `count` and `avg duration` per hour around the spike; `configuration/` `max_concurrent_queries`, workloads/profiles; `metric_log` pool columns.
**Fix:** treat the slowness; raise `max_concurrent_queries` only with headroom; per-user `CREATE QUOTA` or `max_concurrent_queries_for_user`; table-level `max_concurrent_queries`; review workload thread caps (priority is absolute, not proportional).
**Public references:** docs → `max_concurrent_queries`, "Workload scheduling", "Quotas".

### P-21 Timeouts: server limits, client cancels, distributed DDL
**You see:** 159 `TIMEOUT_EXCEEDED` (message says `max_execution_time`, "Watching task /clickhouse/task_queue/ddl/… is executing longer than distributed_ddl_task_timeout", or "Timeout exceeded while reading from socket"); 394 `QUERY_WAS_CANCELLED`; 1002/210 "Broken pipe" on the server side while clients report read timeouts; `ON CLUSTER` DDL taking 180 s then failing.
**Usually means:** (a) the query really exceeds `max_execution_time`; (b) the client's socket timeout is shorter than the query (client cancels, server logs 394/210) — common with `OPTIMIZE … FINAL`, big `INSERT SELECT`, `ALTER` on large tables; (c) `ON CLUSTER` waits for every host in the cluster definition — a dead/renamed host or a stale `system.clusters` entry makes every DDL time out (check `clusters.errors_count`, P-44); (d) a `DROP TABLE` hanging on a table engine with external consumers (Kafka) or waiting for a lock (`DEADLOCK_AVOIDED` on Join/Buffer tables with concurrent inserts).
**Verify:** `query_duration_ms` of the failing hash vs its successful runs; `clusters`; `text_log` for the query id.
**Fix:** align client `receive/send timeout` with realistic durations or run long DDL/`OPTIMIZE` asynchronously; `distributed_ddl_task_timeout`/`distributed_ddl_output_mode = 'none_only_active'`; fix cluster definitions; retry `TRUNCATE`/`DROP` with `lock_acquire_timeout` when other queries hold the table.
**Public references:** docs → `max_execution_time`, `distributed_ddl_task_timeout`, "Distributed DDL".

### P-22 Thread exhaustion symptoms
**You see:** 439 `CANNOT_SCHEDULE_TASK`, 460 `CANNOT_CREATE_TIMER`, 1000 "No thread available" in `errors`/`text_log`; `host_info.cpu.load_avg` ≫ cores; sometimes CPU time flat while concurrent queries climb (lock contention).
**Usually means:** a downstream symptom of saturation — a burst of concurrent queries, a runaway background job, thousands of MV sub-queries, or a version-specific lock-hold increase. The error is a timestamp anchor, not a cause.
**Verify:** what consumed the threads at that time: `query_log_details` count by user/hash, `part_log` merges, `metric_log` pools.
**Fix:** address the consumer; `max_thread_pool_size`/`thread_pool_queue_size` only after that. If CPU stays flat while concurrency and duration grow right after an upgrade, treat as P-54.
**Public references:** docs → server settings `max_thread_pool_size`, `max_concurrent_queries`.

## Bugs, corruption, schema

### P-30 Crash or LOGICAL_ERROR repeating on one version
**You see:** `system.crash_log` rows (signal 11 SIGSEGV / 6 SIGABRT) with the same top frames; `logs/` `<Fatal> BaseDaemon` blocks; `system.errors` 49 `LOGICAL_ERROR` or 1001 `STD_EXCEPTION` (`std::out_of_range`, `unordered_map::at`) with hundreds of occurrences on one code path (often a merge of a table with `JSON`/`Object`/`Nullable` in an output format/projection); the same query hash or job preceding each crash.
**Usually means:** an internal invariant failed — a bug on this build, usually triggered by a specific feature (JSON/Variant/Dynamic columns, projections over them, insert deduplication tokens with row-filtering MVs, a new optimizer path). Some are fixed in a later patch of the same minor.
**Verify:** `version`; whether the crash follows a deterministic trigger (customer job, `MATERIALIZE PROJECTION`, an ALTER); `crash_log.query_id` → `query_log_details`; then `clickhouse-source.md` §3–4 (release notes between the customer's patch and the latest patch of the same minor; issue search on the exact message).
**Fix:** collect **all** `<Fatal>` blocks (not just the header) and the DDL of the table involved; if a fix exists in a later patch, upgrade within the minor first; workarounds seen: drop the projection over a JSON column, avoid the triggering feature, roll back a patch. Never present it as "known bug" without the protocol in `clickhouse-source.md` §4. Note that a very old patch (`.1`/`.2` of a minor) contains none of that minor's bug fixes.
**Public references:** GitHub `ClickHouse/ClickHouse` issues labelled `bug`/`crash`; release notes per patch.

### P-31 Broken/unreadable parts: corruption or version skew
**You see:** `system.detached_parts.reason IN ('broken','broken-on-start','covered-by-broken','unexpected')`; `errors` 40 `CHECKSUM_DOESNT_MATCH`, 226 `NO_FILE_IN_DATA_PART` ("No columns.txt"), 33 `CANNOT_READ_ALL_DATA`, 128/131 "Too large array/string size" while reading a column; logs "N parts broken and need manual correction", "Initialization failed, table will remain readonly … rows in filesystem are suspicious".
**Usually means:** (a) real corruption — disk errors, an interrupted write, hardware; (b) **version skew** — parts written by a newer server (new serialization: sparse columns, packed parts, new Variant/JSON on-disk formats, compatibility-driven MergeTree defaults) read by an older one, typical mid-rolling-upgrade or after a downgrade/restore to OSS from a newer/Cloud build; (c) something environmental masquerading as corruption — e.g. a DNS failure to object storage at restart surfacing as "corrupt file".
**Verify:** versions per host (`system.clusters`, logs banners) and the timeline of the upgrade; whether "broken" parts are all newer than the older node; object-storage reachability at the time.
**Fix:** for (b) finish the upgrade rather than reading with the old binary, set `compatibility`-related settings before downgrading (e.g. `ratio_of_defaults_for_sparse_serialization = 1.0` before going below 23.8), never mix binaries across a warehouse; for (a) `force_restore_data` flag / `SYSTEM RESTORE REPLICA`, re-fetch from healthy replicas, drop `broken*` detached parts only after confirming another replica has the data.
**Public references:** docs → "Self-managed upgrade" (compatibility, downgrade notes), `system.detached_parts`.

### P-32 Schema drift between MV, source and target
**You see:** 53/70 `TYPE_MISMATCH`/`CANNOT_CONVERT_TYPE`, 349 `CANNOT_INSERT_NULL_IN_ORDINARY_COLUMN`, 117 `INCORRECT_DATA`, 10 `NOT_FOUND_COLUMN_IN_BLOCK` in `query_log_details` for `Insert`s with `tables` = an MV target; `part_log` `MutatePart`/`MergeParts` failing with the same codes; 100 % CPU on a query that used to be cheap because the MV column type differs from the target (e.g. `FixedString(32)` vs `String`).
**Usually means:** DDL applied to source, MV and target in the wrong order or with different types; parts written under the old schema while the ALTER committed; JSON path type hints stricter than incoming data.
**Verify:** compare `create_table_query`/`system.columns` of source, MV (`as_select`) and target; mutation `command` timestamps.
**Fix:** align types; when changing types in a chain, pause inserts, alter downstream→upstream consistently, `MODIFY QUERY` the MV, resume; for unreadable parts under a new type: `DETACH PARTITION` to cancel merges, fix, re-attach; make columns `Nullable`/loosen hints rather than reject data.
**Public references:** docs → "Materialized views" (schema changes), `ALTER TABLE MODIFY QUERY`.

### P-33 Inserts "succeed" but rows are missing, duplicated or wrong
**You see:** `query_log_details` `Insert` with `written_rows = 0` but no error; `text_log` "Deduplication path already exists" / 389 `INSERT_WAS_DEDUPLICATED`; `asynchronous_insert_log` with `FlushError` while clients saw OK (`wait_for_async_insert = 0`); MV target empty while source grows (`query_views_log`-style symptom: MV fires, 0 rows read — often a **row policy** on the inserting user); duplicated rows after retries with `insert_quorum`/async; column values shuffled between rows on an old version with `async_insert` + parameterized `INSERT … VALUES ({p:Type})`.
**Usually means:** (a) block deduplication: identical blocks (or the same `insert_deduplication_token` reused for different payloads) are silently skipped; (b) fire-and-forget async inserts lose data on flush failure; (c) a `ROW POLICY` restricts what the insert user can *read* from the source, so the MV writes nothing; (d) a version-specific bug in async-insert template handling (fixed upstream; check the customer's version).
**Verify:** `system.tables` policies are not in the bundle — ask for `system.row_policies`; `Settings` of the inserting user in `configuration/users.d`; `asynchronous_insert_log.status`; version.
**Fix:** `wait_for_async_insert = 1` when loss matters; unique tokens per payload or drop `insert_deduplication_token`; give the insert user the row-policy exemption; upgrade for the template bug.
**Public references:** docs → "Asynchronous inserts", `insert_deduplicate`, `insert_deduplication_token`, "Row policies".

### P-34 MV chains amplify every insert
**You see:** `system.tables`: 5–80 `MaterializedView`s on one source (count `dependencies_table`), 2-hop chains, MVs with `JOIN`/`ARRAY JOIN`/`GROUP BY` onto plain `MergeTree`; `part_log` `NewPart` per hour on MV targets = N × source inserts; TOO_MANY_PARTS reported "while pushing to view …"; `asynchronous_insert_log.p90_flush_ms` in seconds; 241 "while pushing to view" (P-10).
**Usually means:** every insert block is processed synchronously by each MV (and per partition, per projection): `S3 PUTs ≈ 4 × partitions × (1 + projections)` per MV target; async flushes wait for the slowest MV; an MV with `GROUP BY` onto a non-aggregating engine produces duplicate keys; cascaded MVs see the **raw insert block**, not merged state.
**Verify:** MV count per source; target engines; insert frequency (P-02).
**Fix:** batch inserts first; cap MVs per source (~10), consolidate with `ARRAY JOIN` multi-granularity MVs; use `AggregatingMergeTree`/`SummingMergeTree` targets for `GROUP BY` MVs; `dictGet` instead of `JOIN` inside MVs; `Null`-engine staging for heavily partitioned targets; `min_insert_block_size_*_for_materialized_views` to bound memory.
**Public references:** docs → "Materialized views" best practices, "Cascading materialized views".

## Replication, Keeper, network, storage

### P-40 Keeper session loss → read-only replicas
**You see:** `system.replicas.is_readonly = 1` / `is_session_expired = 1`; bursts of 999 `KEEPER_EXCEPTION` (`Session expired`, `Connection loss`, `Operation timeout`) and 242 `TABLE_IS_READ_ONLY` in `errors`/`query_log_details`; `metric_log.zk_hw_exceptions > 0`, `zk_transactions` spiking; `text_log` `ZooKeeperClient`/`ReplicatedMergeTreeRestartingThread` messages; inserts failing, DDL and merges stalling; queries with `select_sequential_consistency = 1` failing during Keeper restarts.
**Usually means:** Keeper (or ZooKeeper) was unavailable, slow or restarting: network partition, saturated Keeper disk (fsync latency), undersized Keeper (memory/CPU, ARM/small volumes), leader re-election storms, a Keeper restart/scale event. After reconnect, replicas normally recover; on some older versions a table stayed read-only until a server restart.
**Verify:** time-correlate `zk_hw_exceptions`, 999 counts and `is_readonly`; `configuration/` Keeper hosts (odd quorum? `session_timeout_ms`?); ask for Keeper logs/`mntr` (`zk_server_state`, `zk_outstanding_requests`, `zk_avg_latency`) — not in the bundle.
**Fix:** stabilise Keeper first (dedicated fast disks, x86 nodes, ≥ 3 nodes, no co-location with heavy ClickHouse I/O); throttle inserts (do not add more writers during saturation); `SYSTEM RESTART REPLICA` / `SYSTEM RESTORE REPLICA` after the outage; `use_xid_64 = 1` on very long-running high-transaction deployments (32-bit xid overflow made long BACKUP queries hang); upgrade Keeper for known reelection fixes.
**Public references:** docs → "ClickHouse Keeper" (configuration, four-letter commands), `system.replicas`, `SYSTEM RESTORE REPLICA`.

### P-41 Replication queue stuck / large absolute_delay
**You see:** `system.replicas.absolute_delay` in minutes–hours, `queue_size`/`inserts_in_queue` high and steady; `replication_queue` with many `GET_PART` (fetch-bound) or `MERGE_PARTS` (merge-bound) entries, `num_tries` in the hundreds, `postpone_reason` "Not executing fetch of part … because N fetches already executing, max N", `last_exception` "No active replica has part … or covering part" (234), "No part … in table" (232), fetch errors 210/209; logs "Will mimic replica …" (log pointer lost — `max_replicated_logs_to_keep` too small).
**Usually means:** the fetch pool is too small for the ingest rate (`background_fetches_pool_size`, default 16, older 8); the source replica no longer has the part (dropped/merged away; stale replica entries → P-43); interserver connectivity/DNS wrong (P-42/P-44); a replica offline so long its log entries were rotated. Merge imbalance between replicas is harmless unless a replica has a *part-count backlog*.
**Verify:** `GROUP BY type` counts; oldest `create_time`; `active_replicas < total_replicas`; `clusters.errors_count`.
**Fix:** raise `background_fetches_pool_size` (server-level on modern versions); fix interserver host/DNS; `SYSTEM RESTART REPLICA`/`RESTORE REPLICA`; increase `max_replicated_logs_to_keep` before rolling upgrades so lagging replicas do not lose their pointer; drop truly dead replicas from Keeper (`SYSTEM DROP REPLICA`).
**Public references:** docs → `system.replication_queue`, `SYSTEM DROP REPLICA`, `background_fetches_pool_size`, `max_replicated_logs_to_keep`.

### P-42 Topology and configuration mistakes
**You see:** `configuration/` without `<interserver_http_host>` on hosts whose hostname resolves ambiguously (fetches go to the wrong cluster → 232/234 in `replication_queue`); duplicated `<macros>` `{replica}` (224 `REPLICA_IS_ALREADY_ACTIVE`); even number of Keeper hosts or a single one; `insert_quorum` larger than the replica count leaving Distributed inserts queued forever (285 on `SYSTEM FLUSH DISTRIBUTED`); a new node in a `Replicated` database that only receives tables created after it joined; `<remote_servers>` listing decommissioned hosts (`ON CLUSTER` timeouts, P-21).
**Usually means:** the topology in config does not match reality.
**Verify:** cross-check `system.clusters` (host_name/host_address/is_local/errors_count) with `<remote_servers>` and `<macros>`; `system.replicas.total_replicas` vs the number of servers.
**Fix:** set `<interserver_http_host>` explicitly (FQDN reachable by peers); unique macros; 3/5 Keeper nodes; remove dead hosts from cluster definitions or use `skip_unavailable_shards`; for a late-joining `Replicated` database replica recreate tables with `CREATE TABLE … AS` + `SYSTEM SYNC REPLICA` (or recreate the database replica).
**Public references:** docs → "Replication" (`interserver_http_host`, macros), "Distributed DDL", `Replicated` database engine.

### P-43 Stale Keeper metadata
**You see:** `system.replicas.total_replicas` > servers that exist; `errors` 253 `REPLICA_ALREADY_EXISTS` when re-creating a table on an existing path, 244/308 unexpected Keeper nodes, 142 `NOT_FOUND_NODE` in logs after a restore; mutations that never finish on one replica with no error (very old block-number nodes); noisy log loops referencing a UUID of a table that no longer exists (orphan catalog entries after a failed `RESTORE`).
**Usually means:** replicas were dropped or servers rebuilt without `SYSTEM DROP REPLICA`; a `RESTORE` re-used a Keeper path; leftovers from years-old operations.
**Verify:** list of replicas known to Keeper vs real hosts (ask for `SELECT * FROM system.zookeeper WHERE path = '<table_path>/replicas'`); `SHOW CREATE` paths.
**Fix:** `SYSTEM DROP REPLICA '<name>' FROM TABLE db.t` for ghosts; restore into a new path/database name (`RESTORE … AS`), or `allow_different_table_def`/`restore` settings as documented; remove orphan nodes only with engineering guidance.
**Public references:** docs → `SYSTEM DROP REPLICA`, "Backup and restore" (replicated tables).

### P-44 Network, DNS and interserver failures
**You see:** `system.clusters.errors_count > 0` / `estimated_recovery_time`; 198 `DNS_ERROR`, 209/210 socket timeouts/resets, 279 `ALL_CONNECTION_TRIES_FAILED`, 519 `NO_REMOTE_SHARD_AVAILABLE`, 574 pending Distributed bytes; `part_log` `DownloadPart` with `error != 0`; logs about hosts in `/etc/hosts` that are not resolvable by DNS; native-protocol clients behind an HTTP proxy failing health checks.
**Usually means:** inter-node connectivity (firewalls, MTU, DNS caching after restarts, proxies that do not speak the native protocol), or a saturated network link between storage and servers (all queries slow, S3 read waits up, P-45).
**Verify:** which hosts appear in the error messages; whether errors are symmetric (both directions) and whether they correlate with restarts (DNS caches).
**Fix:** DNS entries for every node hostname; `<interserver_http_host>`; keep-alives; HTTP interface behind L7 proxies; `skip_unavailable_shards`/`connections_with_failover_max_tries` for resilience; capacity on the storage network.
**Public references:** docs → `system.clusters`, "Distributed table engine" settings.

### P-45 Object storage (S3/Azure/MinIO) throttling, latency, moves and tiering
**You see:** 499 `S3_ERROR` with `SlowDown`/`TooManyRequests`/`Reduce request rate`/`AccessDenied`; `MOVE PARTITION … TO DISK` failing then 84 `DIRECTORY_ALREADY_EXISTS` on retry (leftover `moving/` directories); data unexpectedly landing on the cold/external disk (`system.parts.disk_name`) when the hot disk is near full (`move_factor`, default 0.1); uneven JBOD disk fill; merges stalling with S3 read errors; queries on huge S3 tables spending a fixed ~2 min before reading (per-part read-pool setup on thousands of parts); backups stuck for days; object counts in the bucket far exceeding what `system.remote_data_paths` knows (orphaned blobs after crashes/failed uploads).
**Usually means:** request-rate limits on a prefix, credentials/policy changes after upgrades, storage-policy volumes filling to the move threshold, or too many parts/marks for the object-store round-trip cost.
**Verify:** `system.disks.type` and `free_pct` per volume; `part_log` `MovePart` errors; `text_log` S3 messages by hour; part counts on S3-backed tables.
**Fix:** back-off/retry settings (`s3_max_*`, `s3_retry_attempts`), spread prefixes, fix bucket policies; raise `move_factor` awareness (lower it if you want manual tiering); merge parts down (P-01) before expecting fast reads; enable `system.blob_storage_log` and lifecycle rules for incomplete multipart uploads; investigate orphans with `system.remote_data_paths` vs a bucket listing.
**Public references:** docs → "External disks / S3", "Storage policies" (`move_factor`), `system.blob_storage_log`, `system.remote_data_paths`.

## Query performance and schema design

### P-50 ORDER BY / partitioning design that defeats pruning
**You see:** `system.tables.sorting_key` starting with a high-cardinality column (UUID, trace id, timestamp) or `tuple()`; `query_analysis/text_log_parts` "Selected N/N parts by partition key, … marks" — parts not pruned while granules are; queries slow despite a PK equality; poor compression ratios (HC-4.5); `SELECT *` samples in `query_log_details`.
**Usually means:** the primary index cannot skip whole parts when every part spans the full key range (time-batched inserts + `ORDER BY (id, ts)`), or the leading column is too selective to compress; partitioning is not doing index work (P-03).
**Verify:** compare `sorting_key` with the WHERE shape of the top hashes; `data_compressed_bytes` vs `data_uncompressed_bytes` per table; `partition_key`.
**Fix:** order columns low→high cardinality with the most common filter prefix first; put the time bucket before the id when lookups are time-bounded; `LowCardinality(String)` for < 10k distinct (avoid above 100k); avoid `Nullable` in keys; a projection or skip index for the secondary access path; keep `SELECT` to needed columns.
**Public references:** docs → "Choosing a primary key", "Sparse primary indexes" guide, "Skip indexes".

### P-51 FINAL on Replacing/Collapsing tables
**You see:** `query` samples with `FINAL` on large `ReplacingMergeTree`/`CollapsingMergeTree` tables; `read_rows/count` ≈ table row count even with small `LIMIT`; high memory; occasionally empty results with `FINAL` after an upgrade (version-specific bug — check release notes).
**Usually means:** `FINAL` must read and deduplicate all rows before `LIMIT`; projections are never used with `FINAL`; PREWHERE auto-move needs `optimize_move_to_prewhere_if_final = 1`.
**Verify:** compare `read_rows` of the FINAL hash to `sum(rows)` in `system.parts`; part count of the table (more parts = more work).
**Fix:** `do_not_merge_across_partitions_select_final = 1` with a partition key that bounds duplicates; `argMax`/`GROUP BY` dedup patterns; a dedicated MV ordered for the query; `min_age_to_force_merge_seconds` (+ `_on_partition_only`) to keep the table merged; `OPTIMIZE … FINAL` in quiet windows.
**Public references:** docs → "FINAL modifier", ReplacingMergeTree.

### P-52 GROUP BY / JOIN memory and the join build side
**You see:** 241 with `AggregatingTransform` (GROUP BY) or `FillingRightJoinSide`/`HashJoin` (JOIN) in the message; the same hash intermittently OOMs (successful runs use 100 MB, failing runs 20 GB) — a join-order optimizer choosing a big build side when statistics are missing; `INSERT … SELECT` with aggregation over billions of rows failing; `uniqExact`/`COUNT(DISTINCT)` on high-cardinality columns.
**Usually means:** the aggregation hash table or the JOIN right side does not fit; `max_insert_block_size` does not apply to `INSERT SELECT`.
**Verify:** query sample; `Settings` in `configuration/users.d`; version (join-order optimizer behaviour changed across 25.x–26.x).
**Fix:** `max_bytes_before_external_group_by`/`max_bytes_ratio_before_external_group_by` (spill; keep `max_memory_usage` ≈ 2×); smaller right side or `dictGet`; `join_algorithm = 'grace_hash'|'partial_merge'`, `max_bytes_in_join`; pin join order (`query_plan_optimize_join_order_limit = 0` or explicit order) when the optimizer misjudges; `uniq`/`uniqCombined64` instead of exact; split `INSERT SELECT` by key ranges; hash long string GROUP BY keys.
**Public references:** docs → "GROUP BY in external memory", "JOIN" (algorithms), "Memory limit exceeded" KB.

### P-53 Indexes not used and analyzer behaviour changes
**You see:** `tokenbf_v1` index in DDL but queries use `LIKE '%substring%'` (token index cannot help; needs `hasToken` or `ngrambf_v1`); several skip indexes but `OR` conditions across columns (needs one composite index); `x IN arrayMap(...)`/complex expressions not using the primary key; after an upgrade: `CYCLIC_ALIASES` (174), `COLUMNS()` order change, `lambda()` legacy syntax, `Unknown expression identifier` — behaviour differences of the new analyzer (default since 24.3).
**Usually means:** the index type does not match the predicate, or the analyzer changed semantics.
**Verify:** DDL `INDEX` definitions vs query samples; version; `text_log_parts` granule counts.
**Fix:** `ngrambf_v1` for substrings, `hasToken*` for tokens, composite indexes for `OR`; rewrite constant array expressions; as a diagnostic, run the query with `enable_analyzer = 0` (≤ 24.x: `allow_experimental_analyzer = 0`) to confirm an analyzer-related change, then adapt the query rather than keeping the legacy path.
**Public references:** docs → "Data skipping indexes", "Analyzer" (behaviour changes).

### P-54 Regression after an upgrade
**You see:** `logs/` show a version change; from that hour `query_log_details` `query_duration_ms/count` or `memory_usage/count` for the *same* `normalized_query_hash` jumps (×1.5–×2), 241/202/159 rise, `part_log` merge durations change, CPU flat while concurrency climbs (lock contention), `text_log` volume explodes (a log line emitted far more often), `system.tables` read slow (catalog behaviour changes), PK pruning lost through views/row policies, lightweight-update patch parts disabling an optimisation; sometimes only a *mixed-version* window is bad (rolling upgrade, replicas on two versions).
**Usually means:** a genuine version regression, a changed default (`compatibility` setting), or expected transitional cost (cold caches after restart, mixed versions, re-merging with new formats).
**Verify:** before/after comparison of ProfileEvents for one hash (`query_analysis/profile_events_compare` if the window spans both); `SettingsChangesHistory` for the versions; release notes between the two versions (`clickhouse-source.md` §3); whether all nodes are on the same version.
**Fix:** finish the rollout so no mixed window remains; set `compatibility = '<old version>'` to restore old defaults while investigating (note it also flips MergeTree defaults that need a restart); roll back within the supported window if the regression is confirmed and blocking; report with the two-version ProfileEvents diff. Prefer LTS minors (`YY.3`, `YY.8`) and the latest patch of the chosen minor.
**Public references:** docs → "Self-managed upgrade", `compatibility` setting, changelog "Backward Incompatible Change" sections.

### P-55 Monitoring and internal tooling dominates the query load
**You see:** `query_log_details` by `user`: monitoring/exporter/scraper users with the most `count` and sometimes the largest `memory_usage`; patterns like `SELECT * FROM system.tables`, `DESCRIBE`, `system.parts` scans every few seconds on catalogs with tens of thousands of tables; merges of `system.metric_log` failing (P-17); 241 on "small" user queries because monitoring holds memory.
**Usually means:** observability tooling scrapes too often or too broadly for the catalog size; some versions made `system.tables` reads more expensive (e.g. computing `total_rows`).
**Verify:** `count` and `memory_usage/count` per user; `query` samples of the top internal hashes.
**Fix:** lower scrape frequency, select only needed columns/tables, avoid `SELECT *` on `system.tables`/`system.columns` with huge catalogs, use `system.metrics`/`system.asynchronous_metrics` instead of scanning log tables; put monitoring users under a memory/profile limit.
**Public references:** docs → "Monitoring" (Prometheus endpoint), `system.asynchronous_metrics`.

### P-56 Distributed load (`s3Cluster`, `INSERT … SELECT`) is slow — shards, file skew, initiator-side insert
**You see:** a "slow SELECT" hash whose `query_details.tables` is `_table_function.s3Cluster` (or `s3`, `url`, `remote`) and `is_initial_query = 0`; several executions in the same minutes on different replicas with `read_rows` proportional to `query_duration_ms`; `text_log_full` shows `Reading object …` lines with very unequal file sizes, then one long silence until `Source finished: files_read=N`; in `profile_events`, `NetworkSendElapsedMicroseconds` is the majority of `RealTimeMicroseconds`, `ParquetFetchWaitTimeMicroseconds` is large, and `OSCPUWaitMicroseconds` ≈ `OSCPUVirtualTimeMicroseconds`; `part_log` shows the target table receiving all `NewPart` events on one replica (`hostname`) plus `DownloadPart` fan-out to the others.
**Usually means:** the statement is `INSERT INTO t SELECT * FROM s3Cluster(...)`. Each replica reads a subset of *files* and streams decoded rows back to the **initiator**, which does all the inserting; wall time = the slowest replica's share, and shares are decided per file, so a few large files dominate. Decoding Parquet is CPU-heavy and shows up as CPU wait on a throttled pod. The "fast" execution the tool paired is just the replica that got the small file.
**Verify:** `read_rows/duration` roughly equal across replicas (skew, not a slow node); file sizes in the `Reading object` lines; `NewPart` for the target concentrated on one `hostname` in `part_log`.
**Fix / mitigation:** `SET parallel_distributed_insert_select = 2` so every replica inserts its own share locally (removes the return trip that is most of the wall time); split the source into many similar-sized files (≥ 3× the replica count) so file distribution balances; run bulk loads when the service is not CPU-capped or scale up for the duration; `max_insert_threads`/`max_threads` do not help while CPU wait is high. For one-off loads consider plain `s3()` on one replica with `max_download_threads` if the dataset is a handful of files anyway.
**Not to be confused with:** P-21 (client timeouts on the same statement), P-52 (memory — here tracked memory is small, untracked Arrow allocations are large), P-54 (only if the same load got slower after an upgrade).
**Public references:** docs → `s3Cluster` table function, `parallel_distributed_insert_select`, "Inserting data from S3" guide (file layout and parallelism).

### P-57 Keeper quorum loss or leader flapping — the session-expiry cascade
**You see:** `metric_log_coordination_3_days` → `sum(ProfileEvent_ZooKeeperHardwareExceptions)` jumps from 0 to millions in one hour while `sum(ProfileEvent_ZooKeeperTransactions)` collapses; `query_log_details` → the same hours fill with 999 `Session expired` on inserts, 319 `UNKNOWN_STATUS_OF_INSERT`, then 571 `DATABASE_REPLICATION_FAILED` and 57 `Mapping for table with UUID … already exists` on `CREATE OR REPLACE TABLE db.`.tmp.inner_id.<uuid>`` (`/* ddl_entry=query-N */`), and finally 252 `TOO_MANY_PARTS` on the busiest insert target; `part_log_3_days` → `MergeParts` with `error = 999` in the outage hours, then hours with `NewPart > 0` and `MergeParts = 0`; `system.errors` → 221 `NO_SUCH_INTERSERVER_IO_ENDPOINT` and 86 `RECEIVED_ERROR_FROM_REMOTE_IO_SERVER` in the tens of thousands; `zookeeper_connection` → `session_uptime_elapsed_seconds` of minutes on a server up for months; `zookeeper_log_errors_1_day` → `sessions` per hour > 2, `error = ZSESSIONEXPIRED` / `ZCONNECTIONLOSS`.
**Usually means:** the Keeper ensemble lost quorum or kept re-electing its leader (a Keeper node out of memory or disk, two of five members unable to sync, a forced leadership transfer, network partition). Every ClickHouse session expired at once; inserts into replicated/shared tables could not commit; merges — assigned through Keeper — stopped, so parts piled up until `parts_to_throw_insert`; `Replicated` databases replayed their DDL log once the session returned, colliding with their own half-applied `CREATE`s (57) and, when a session died mid-replay, failing outright (571). The interserver 221/86 storm is the replicas re-discovering each other's endpoints after restarts and is recovery noise unless it persists.
**Verify:** the Keeper health test (health-checks HC-3.8) says *unavailable*, not *blip*, for the hours in question (exceptions > 1000/h **and** transactions < 50 % of the 7-day median); the hours line up across *all three* of metric_log, part_log errors and query_log exceptions (one source alone can be a single table); `system.databases` `Replicated` count and `system.tables` count give the Keeper load multiplier (hundreds of Replicated databases × tens of replicas = a very large watch/session set); `configuration/zookeeper.xml` lists the ensemble (odd? 5?); `text_log_histogram` `DDLWorker`/`DatabaseReplicated`/`ZooKeeperClient` Error counts per hour. Keeper-side confirmation needs the Keeper logs and `echo mntr | nc <keeper> <port>` from each member — the bundle cannot show them.
**Fix / mitigation:** stabilise the ensemble first (restore an odd, in-sync quorum; a five-member ensemble with two members unable to sync should be brought back to three healthy members before adding new ones; size Keeper memory for the snapshot + log and set `max_memory_usage_soft_limit`); then let ClickHouse recover — `SYSTEM RESTART REPLICA` for tables still read-only, a rolling restart for servers whose sessions never re-established; re-run the failed DDL only after checking `distributed_ddl_queue`. Reduce the blast radius afterwards: fewer `Replicated` databases per Keeper (or a dedicated ensemble per cluster), `insert_keeper_max_retries`/`insert_keeper_retry_*` so inserts ride out short losses, and Keeper alerting on `zk_followers`/`zk_synced_followers` and leader changes.
**Not to be confused with:** P-40 (a single replica's session loss — here it is every table on every replica at once), P-01 (a merge backlog with a healthy Keeper — here merges are *absent*, not slow), P-42 (a permanently misconfigured ensemble — here it worked until the incident).
**Public references:** docs → ClickHouse Keeper (quorum, `four_letter_word_white_list`, `mntr`), `insert_keeper_max_retries`, Replicated database engine, `SYSTEM RESTART REPLICA`; ClickHouse/ClickHouse issues mentioning "Session expired" storms are common — cite one only after the four-step protocol in `clickhouse-source.md`.

### P-58 After the incident: 107 FILE_DOESNT_EXIST / `The specified key does not exist` on Active parts
**You see:** `query_log_details` → 107 `File data/<uuid>/<part>/<file> doesn't exist … attempt to read data part <part> (state Active) failed … Please retry the query` on many tables in the hours **after** Keeper recovered, or `The specified key does not exist. This error happened for S3 disk … while reading key: <bucket key>`; `system.disks.type = ObjectStorage` for the disk in the message and `storage_policies` puts user tables on it (an `s3_with_keeper` / `s3_plain_rewritable` disk, or a `cache` disk over one); `system.metrics` (cloud mode, per replica) → `MetadataFromKeeperCacheObjects` in the hundreds of thousands on most replicas and ≈ 1 on a few; `blob_storage_log_7_days` → a `Delete` for the missing key hours or days earlier, or a `failed = 1` Upload; `part_log` → 84/504 `*_ALREADY_EXISTS` retries during the outage.
**Usually means:** the part's *metadata* and its *blobs* disagree. On object-storage disks a part is a set of blobs plus a metadata layer (local metadata files, or metadata kept in Keeper with a per-server cache). After a Keeper outage a server can hold a stale view of that metadata — pointing at blobs a merge on another replica has since deleted, or at blobs an interrupted commit never wrote — and every read of that part fails until the view is refreshed. Servers whose cache was emptied (the ≈ 1 replicas) rebuild it lazily and hit the same error until they do. Actual corruption is rare in this picture; the "please retry" wording is literal.
**Verify:** the 107 tables all live on the object-storage policy (join `system.tables.storage_policy`); the failing file kinds are metadata-ish (`primary.idx`, `*.cmrk*`, `checksums.txt`) rather than one column across one part; a `Delete` in `blob_storage_log` for the key (or `SELECT local_path, remote_path FROM system.remote_data_paths WHERE remote_path LIKE '%<key>%'` run by the owner); the errors cluster on the replicas with the anomalous cache metric; the *same* query succeeds on another replica.
**Fix / mitigation:** refresh the metadata view rather than touching data: `SYSTEM RESTART REPLICA db.table` for the affected tables; on disks with Keeper-held metadata `SYSTEM DROP DISK METADATA CACHE <disk>` on the affected servers (then confirm the cache metric grows again); a rolling restart of the servers that still fail; only then consider `CHECK TABLE` / `SYSTEM SYNC REPLICA` / detach-and-fetch for a part that fails on every replica. Report to ClickHouse with the exact message, disk type and version — this class is version-sensitive.
**Not to be confused with:** P-45 (genuinely broken parts moved to `detached/` with `broken*` reasons — here parts stay `Active`), P-19 (a full local disk), P-57 (the cause; this pattern is its aftermath), a 403/credentials error on the same disk (`system.events` `S3*RequestsErrors` with `Forbidden` in messages).
**Public references:** docs → "External disks for storing data" (S3 disk types, metadata types), `SYSTEM DROP DISK METADATA CACHE`, `SYSTEM RESTART REPLICA`, `system.blob_storage_log`, `system.remote_data_paths`.

---

## Adding a pattern

Write it symptom-first and bundle-observable: **You see** (file → column → condition) · **Usually means** · **Verify** (a second, independent source) · **Fix / mitigation** (settings with defaults and version notes) · **Not to be confused with** · **Public references** (docs/GitHub only). Strip every identifier: organisation, service, hostname, database/table/column names, users, case or ticket numbers, people. Add the row to the quick-reference table and cross-link from `health-checks.md`/`error-codes.md` where a check or code points at it.
