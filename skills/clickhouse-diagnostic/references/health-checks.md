# Health checks — the rule set for turning a bundle into findings

Run these in order after the coverage statement (SKILL.md step 0). Each check names the file/column, the threshold, the severity it produces, what it usually means, and what to do next. Thresholds are aligned with the tool's own alerts (`alerts/*.yaml`) and dashboard colouring, so the skill never contradicts `dashboard.html`; the remaining thresholds come from ClickHouse defaults and field experience and are marked *(guideline)*.

Severity vocabulary: **critical** = data unavailable/at risk now · **warning** = degrading or will fail soon · **info** = worth knowing, no action required · **ok**. A check whose input file is absent/empty-by-design is **n/a** (never "ok"). Always state the evidence rows (`file → column → value`) next to the verdict; recipes are in `reading-recipes.md`, pattern ids in `known-patterns.md`.

Only sum `system.query_log_details_7_days` after fixing one `tables` value (see bundle-layout §4). Only read `system.parts` with `active = 1`.

---

## HC-0 Coverage (gate — do this first)

| Check | Source | Verdict |
|---|---|---|
| Version and mode known | `system.version`, file set (bundle-layout §3) | state them |
| Window actually covered | `min/max(time)` in `query_log_details`; `min(event_time)` in `text_log` | if the user's incident is outside the window → say so and propose a `-from/-to` re-run (`running-the-tool.md`) before any conclusion |
| Empty/absent files | `wc -l` | distinguish "empty because healthy" (`crash_log`, `merges`, `replication_queue`, `detached_parts`) from "absent because not collected" (gov, version gate, table disabled) |
| Truncated logs | `logs/*` first line `### support-diagnostic: TRUNCATED` | note that the first surviving line is not the start |
| One node of a SharedMergeTree cluster | `system.settings` `cloud_mode = 1`, engines `Shared*MergeTree` in `system.tables`, `system.clusters` `default` with N hosts, mode `onprem` | the bundle describes **one replica of N** (parts, errors, part_log, query_log, text_log are per replica); say so, and propose a `-mode cloud` re-run for cluster-wide evidence |
| Host facts describe the right machine | `host_info.os.hostname` vs `system.clusters.host_name` / logs hostnames | if the tool ran on a laptop against a remote server, ignore `host_info` and say so |
| Collectors that did not run | `execution_log.txt` *Failed collectors* | name each with its error; a missing result file is one of these, not an empty table. Code 159 = the tool's `-query-timeout`; 497 = grant; 60/139 = table or config not present on this server |
| Collector's own timeouts | `query_log_details` / `system.errors` code 159 from the collector's user at collection time | the collector's `-query-timeout` (default 240 s) fired on a slow system table — that file is partial; not a customer problem |
| Collector's own grants | `system.errors` code 497 near collection time; `system.databases` only `system`? (`system.tables` distinct `database`) | a bundle showing only `system` tables likely lacks `SHOW DATABASES/TABLES` — the bundle is incomplete, not the server empty |

## HC-1 Availability and crashes

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 1.1 | `system.crash_log` (or dashboard `crash_log`) | any row | **critical** | Server received a fatal signal. Read `signal`, `version`, `query_id`, top of `trace_full`; find the query in `query_log_details` by hash if `query_id` known; check `logs/` `<Fatal>` lines for the same time. Repeated identical stacks on one version → P-30. |
| 1.2 | `logs/clickhouse-server.log` | `Starting ClickHouse` / `Ready for connections` count > 1 in the window, or gaps between `Received termination signal` and next start | warning | Restarts. Correlate with `crash_log`, OOM-killer (`host_info` notes, ask for `dmesg`), or deploys. |
| 1.3 | `host_info.os.uptime_seconds` vs dashboard `uptime` | server uptime ≪ host uptime | info | Server restarted more recently than the host — expected after upgrades, otherwise ask why. |
| 1.4 | `system.replicas` | `is_readonly = 1` | **critical** (alert `replica_readonly`) | Replica cannot accept inserts: Keeper session lost (`is_session_expired = 1`), metadata mismatch, or disk full. Do not infer which — `zookeeper_exception` carries the last exception raised while fetching info from Keeper and `last_queue_update_exception` the last queue-update failure (e.g. log entries this version cannot parse); both are hashed in gov and are absent from bundles collected before that column set. → HC-3, HC-4, P-40. |
| 1.5 | `system.errors`, `system.error_log_7_days` (≥ 24.8) | `TABLE_IS_READ_ONLY` (242) or `KEEPER_EXCEPTION` (999) with recent `last_error_time`; in `error_log` the hours where these codes burst | warning→critical | Same family as 1.4; see the counts relative to uptime (`asynchronous_metrics.Uptime`), and use `error_log` for the timeline — it includes background threads. |

## HC-2 Parts and merges

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 2.1 | `system.parts` active, per `(database, table, partition_id)` | `count() > 300` | warning (alert `too_many_parts`); `≥ 500` shows red in the dashboard; ≥ the table's `parts_to_throw_insert` (OSS default 3000 on ≥ 23.6, older 300; Cloud 10000 — read `engine_full`) | **critical** | Inserts will be delayed (`parts_to_delay_insert`, default 1000/150) then rejected with 252. Find the cause: many small inserts (2.3), merges blocked (2.5/2.6), partition fan-out (2.4). P-01…P-05. |
| 2.2 | `system.parts` active | `countIf(level = 0) > 100` per table *(guideline)* | warning | Unmerged insert blocks piling up — merges not keeping up *right now*. P-01. |
| 2.3 | `system.parts` active; `part_log` `NewPart` | median `rows` per active part `< 10 000` with hundreds of parts, or NewPart `size_in_bytes/count` in KB | warning | Tiny inserts. Batch ≥ 10k–100k rows or enable async inserts (HC-7). P-02. |
| 2.4 | `system.tables.partition_key` + parts | > 1000 partitions per table, or a partition key with high cardinality (hour/minute/tenant-id) *(guideline)* | warning | Partitioning is a lifecycle tool, not an index: each insert touching N partitions creates N parts; merges never cross partitions. P-03. |
| 2.5 | `system.merges` | `elapsed > 3600` with `progress < 0.5`, or `memory_usage` near limits | warning | A stuck/huge merge occupying a pool slot; often the reason 2.1 grows while the pool "looks idle". P-04. |
| 2.6 | `part_log` | `event_type = 'MergeParts' AND error != 0` — read `error` (241 = OOM, 236 = aborted, 243 = disk, 1001/49 = bug, 40/226 = corruption) and `distinct_exceptions` | warning→critical | Failing merges loop and pin their parts (mutations behind them stall too). Code 241 on merges → P-11; 1001/49 → P-30; 40/226 → P-31. |
| 2.7 | `metric_log` | `max_merge_pool_tasks` at `background_pool_size` (default 16; check `configuration/`) for consecutive hours *(guideline: pool value > 256 anywhere = overloaded)* | warning | Merge pool saturated → parts accumulate. Either more CPU/pool or fewer/larger inserts. P-01. |
| 2.8 | `part_log` `MergeParts` `size_in_bytes/count` | average merge result tiny (KBs–MBs) while part counts high | info | Merges are busy but ineffective — same as 2.3 upstream. |
| 2.9 | `system.parts` active | `bytes_on_disk > 150 GiB` | warning (alert `large_parts`) | Above `max_bytes_to_merge_at_max_space_in_pool` — the part will never be merged again; `OPTIMIZE FINAL` ignores the cap. Usually fine; matters for mutations (each rewrite touches the whole part) and disk headroom. |
| 2.12 | `system.part_log_3_days` | an hour with `NewPart > 100` and `MergeParts (error = 0) = 0` (alert `merges_stalled`) | warning → **critical** if ≥ 3 consecutive hours | Inserts kept creating parts while nothing merged. Look at the same hours in `metric_log_coordination` (`ZooKeeperHardwareExceptions`, `ZooKeeperSession = 0`) — on replicated/shared tables merges are assigned through Keeper — then at the merge pool (2.7) and `text_log_histogram` (`Temporarily pause scheduling`). P-01, P-57. |
| 2.10 | `system.parts` | `part_type = 'Compact'` dominating a large table | info | Compact parts read every column; `min_bytes_for_wide_part` default 10 MiB (Cloud 1 GiB). Only a finding if queries are column-selective and slow. |
| 2.11 | `system.detached_parts` | any row | info (alert `detached_parts_exist`); `reason IN ('broken','broken-on-start','covered-by-broken')` → warning | `broken*` = corruption/interrupted write (P-31); `ignored`/`clone`/`unexpected` = replication bookkeeping, usually safe to drop after review. Sum `bytes_on_disk` — detached parts still consume disk. |

## HC-3 Replication and Keeper

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 3.1 | `system.replicas` | `absolute_delay > 60` s | warning (dashboard) ; `> 3600` **critical** *(guideline)* | Replica behind the log. Check `queue_size`, `inserts_in_queue` vs `merges_in_queue` (fetch-bound vs merge-bound), `queue_oldest_time`. P-41. |
| 3.2 | `system.replicas` | `is_session_expired = 1` | **critical** | Keeper session lost → read-only until re-established. HC-3.5. |
| 3.3 | `system.replicas` | `active_replicas < total_replicas` | warning | A replica is down/unreachable; `total_replicas` also counts replicas that were never dropped from Keeper (P-43). |
| 3.4 | `system.replication_queue` | any `type` with `count() > 60` *(guideline)*, or `last_exception != ''` (alert `replication_queue_errors`), or `create_time` older than 1 h with `is_currently_executing = 0` | warning→critical | Stuck queue entries. Read `postpone_reason` (e.g. "Not executing … because N merges already executing", "Not merging because part … is not ready", missing part) and `last_exception` (fetch failures 210/209, 40/226 corruption). `num_tries` counts failed attempts on that entry, so a high value with `is_currently_executing = 0` is retry-and-fail rather than waiting its turn. P-41, P-44. |
| 3.5 | `metric_log` | `zk_hw_exceptions > 0` in any hour; `zk_transactions` spikes ×5 vs baseline *(guideline)* | warning | Keeper connection loss/timeouts. Correlate with 999 in `errors`/`query_log_details` and `text_log` `ZooKeeperClient`/`Session expired`. P-40. |
| 3.6 | `system.clusters` | `errors_count != 0` | warning (dashboard) | Distributed queries failed to reach this host recently (`estimated_recovery_time` counts down). P-44. |
| 3.7 | `configuration/` | `<zookeeper>`/`<keeper_server>` hosts count even or 1 in production; `<macros>` duplicated across hosts | warning | Keeper needs an odd quorum (3 or 5); duplicate `{replica}` macros → 224 REPLICA_IS_ALREADY_ACTIVE. P-42. |
| 3.8 | `metric_log_7_days` (`zk_hw_exceptions`, `zk_transactions`) or `metric_log_coordination_3_days` | **Keeper health test, per hour** (see the block below): hardware exceptions > 1000 **and** transactions < 50 % of the 7-day median → *unavailable* (alert `keeper_health`); exceptions > 1000 with ≥ 50 % traffic → *blip* (alert `keeper_connection_blips`); transactions < 10 % of median with no exceptions → *idle or disconnected* (check `ZooKeeperSession`) | **critical** / warning | The client side of a Keeper outage, leader election or partition: sessions expired, requests timed out, the work that flows through Keeper stopped. Name the hours; every other finding in those hours (252, 319, 571, 57, stalled merges) is downstream. P-40, P-57. |
| 3.9 | `system.zookeeper_connection` | `is_expired = 1`; or `session_uptime_elapsed_seconds` ≪ server `Uptime` (`asynchronous_metrics`) | critical / info | No session now / the session was re-established at *connected_time* — the moment of the last expiry. Cloud: a replica whose `host` or session age differs from its peers. |
| 3.10 | `system.zookeeper_log_errors_1_day`; latency from `metric_log_coordination` | `error IN (ZSESSIONEXPIRED, ZCONNECTIONLOSS, ZOPERATIONTIMEOUT)` rows in an hour; `sessions_affected` > 1; `sum(ProfileEvent_ZooKeeperWaitMicroseconds) / sum(ProfileEvent_ZooKeeperTransactions)` per hour ×10 vs its median | warning→critical | Which operations failed and how many sessions were hit; whether Keeper latency degraded *before* the failures (saturation first). `ZNONODE`/`ZNODEEXISTS`/`ZBADVERSION` in volume are normal control flow. Absent file = table not enabled. |
| 3.11 | `system.distributed_ddl_queue` | `status != 'Finished'` older than 10 min; `exception_code IN (57, 571, 999)` bursts; the same `query` (a `CREATE OR REPLACE … .tmp.inner_id.*`) repeated across many `entry` values | warning | Stuck or replayed DDL: Replicated databases re-run their DDL log after a session loss; `57` with "Mapping for table with UUID … already exists" is the replay colliding with the first attempt. P-57. |
| 3.12 | `system.metrics` (cloud: per `hostname`) | `ZooKeeperSession = 0`; `ReadonlyReplica > 0`; `MetadataFromKeeperCacheObjects` orders of magnitude apart between replicas | critical / warning | No session / read-only tables *now*; on object-storage-with-Keeper disks a replica whose metadata cache is nearly empty while peers hold hundreds of thousands of objects has lost or dropped its cache and will read stale metadata (`FILE_DOESNT_EXIST` 107, `The specified key does not exist`). P-58. |
| 3.14 | `system.replicated_fetches` | fetches with `elapsed` > 600 s, or the same `result_part_name` at the same `progress` in two bundles | info→warning | Catching up after an outage (expected, explains `absolute_delay`) vs a wedged interserver connection (P-41). |
| 3.13 | `system.databases` | `engine = 'Replicated'` count in the hundreds, × replicas | info | Each Replicated database is a DDLWorker, a Keeper watch set and a DDL log per replica — a Keeper-load multiplier worth naming when Keeper is the finding. |

### Keeper health — the review to run whenever 999, 319, 571 or read-only replicas appear

Keeper problems are judged on **two counters per hour**, never on one: `ZooKeeperHardwareExceptions` (how many requests failed for connection reasons) and `ZooKeeperTransactions` (how much work Keeper actually completed). Both are in `metric_log_7_days` (`zk_hw_exceptions`, `zk_transactions`) and, with many more, in `metric_log_coordination_3_days`. Reference numbers: a busy server does 10⁶–10⁷ transactions/hour; a healthy hour has 0 hardware exceptions.

| Hour looks like | Verdict | Then read |
|---|---|---|
| exceptions 0, transactions ≈ median | healthy | — |
| exceptions > 1000, transactions ≥ 50 % of median | **blip** — a session was lost and re-established (Keeper leader change, one Keeper node restarted, network hiccup) | `text_log` / `text_log_keeper_1_day`: `Finalizing session`, `Connected to ZooKeeper at <host>` (did the server move to another Keeper host?); `part_log` 999 in that hour; `zookeeper_connection.connected_time` |
| exceptions > 1000 **and** transactions < 50 % of median (often < 10 %) | **unavailable** — sessions expiring and no work completing: quorum lost, leader flapping, Keeper out of memory/disk | the whole cascade: `query_log` 999/319 → 571/57 → 252 by hour; `part_log` `MergeParts` 999 then hours with `NewPart` and no `MergeParts`; `zookeeper_log` `ZSESSIONEXPIRED`, `sessions/h`; `system.errors` 221/86 on recovery. Ask for Keeper `mntr` output and Keeper logs (P-57) |
| exceptions 0, transactions < 10 % of median | **idle or disconnected** | `system.metrics.ZooKeeperSession` (0 = no session), `zookeeper_connection.is_expired`; if the server was simply idle (night, weekend) say so |
| mean Keeper latency (`ZooKeeperWaitMicroseconds / ZooKeeperTransactions` per hour from `metric_log_coordination`) ×10 in the hours *before* the failures, or `zookeeper_log_errors` `ZOPERATIONTIMEOUT` before `ZSESSIONEXPIRED` | **Keeper saturated first** — latency rose, then sessions timed out | Keeper memory/disk/snapshot pressure, request volume (`ZooKeeperTransactions`/h), `Replicated` database count × replicas (HC-3.13) |

Markers to grep in `text_log` / `logs/` for the exact minute (all from `ZooKeeperClient` / `clickhouse-serv` threads): `Session expired`, `Finalizing session <id> … reason:`, `Connected to ZooKeeper at <host>:<port> with session_id`, `Trying to establish a new connection`, `ZooKeeperSend … Code: 210 … Broken pipe` (the socket to Keeper died). On the Keeper side the customer can run `echo mntr | nc <keeper> <port>` on every member: `zk_server_state` (one leader), `zk_followers` = `zk_synced_followers`, `zk_avg_latency` / `zk_max_latency`, `zk_outstanding_requests`; Keeper logs show `Receiving request for session N took X ms` before a saturation-driven expiry and election messages during leader flapping.

Distinguish from **client-side** Keeper noise: 999 with `Bad version` / `No node` / `Node exists` are ordinary optimistic-concurrency retries (P-40 §) and never produce hardware exceptions.

## HC-4 Disk and storage

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 4.0 | `system.disks.type`, `system.storage_policies` | any `ObjectStorage` disk in a policy used by user tables | info (context) | Data lives on S3/Azure/GCS: local `free_space` is only the cache/metadata volume; "file doesn't exist" errors mean a blob or its metadata, not a local file. Read 4.x with that in mind and open `blob_storage_log_7_days` if present. |
| 4.9 | `system.blob_storage_log_7_days` | `failed = 1` rows; an hour with `Delete` counts far above baseline right before 107/`NoSuchKey` errors | warning→critical | Failed uploads = parts whose data never reached the bucket; a delete burst followed by reads of the same keys = metadata (local or in Keeper) pointing at removed blobs. P-58. |
| 4.10 | `system.events`, `metric_log_coordination` | `S3ReadRequestsErrors`, `ReadBufferFromS3RequestsErrors`, `S3WriteRequestsErrors`, `DiskS3*Errors` > 0 and rising | warning | Object-storage request failures (404 = missing key, 403 = credentials/policy, 5xx/timeouts = endpoint). Split by hour with `metric_log_coordination`. |
| 4.1 | `system.disks` | `free_pct < 15` | **critical** (alert `disk_space_low`); `< 5` red | Merges need free space ≈ result size; below `min_free_disk_ratio_to_perform_insert`/`keep_free_space_bytes` inserts fail with 243 and replicas can go read-only. Find what to free: `system.parts` by table, `detached_parts`, `logs/` size, system log tables (`system.*_log` in `system.parts`). |
| 4.2 | `host_info.disks` | `used_pct > 85` on the mount holding `<path>` (from `configuration/`) *(guideline)* | warning | Same as 4.1 seen from the OS; also catches the log volume filling up. |
| 4.3 | `system.disks.type` | `s3`/`object_storage` disks present | info | S3-backed: expect 499 errors under throttling; merges and reads depend on object-store latency. Enables P-45 checks. |
| 4.4 | `system.parts` by `database` | `system` database among the top-3 by `sum(bytes_on_disk)` *(guideline)* | warning | System log tables without TTL (`text_log`, `trace_log`, `query_log`, `metric_log`) eat disk and merge capacity; their merges can even OOM (P-17). Recommend TTL in `<query_log>`/`<text_log>` config. |
| 4.5 | `system.parts` | `data_uncompressed_bytes / data_compressed_bytes < 2` on large tables *(guideline)* | info | Poor compression — ORDER BY not grouping similar values, wrong codecs, high-entropy strings. P-50. |

## HC-5 Memory and CPU

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 5.1 | `metric_log.avg_memory_tracking_bytes` vs `host_info.memory.total_bytes` / `cgroup_memory_limit_bytes` / `max_server_memory_usage(_to_ram_ratio)` in `configuration/` | hourly average > 80 % of the effective limit *(guideline)* | warning | Sustained pressure → 241 on queries and merges. Split by consumer: per-query memory (5.3), merges/mutations (`part_log.peak_memory_usage`), caches (`configuration/` `mark_cache_size`, `uncompressed_cache_size`), dictionaries (`system.dictionaries.bytes_allocated`), primary keys in memory (`system.parts.primary_key_bytes_in_memory_allocated` summed). P-10…P-13. |
| 5.2 | `host_info.memory` | `available_bytes < 2 GiB` | warning (mirrors ClickHouse's own startup warning) | Host is out of RAM headroom; OS may swap or OOM-kill the server. |
| 5.3 | `query_log_details` | `exception_code = 241` count and share; the `normalized_query_hash` with highest `memory_usage/count` | warning; **critical** if 241 hits inserts or is > 5 % of queries *(guideline)* | Read the message class in `exception`: "(for query)" → per-query limit (`max_memory_usage`), "(total)" → server limit, "User memory limit" → `max_memory_usage_for_user`, "while pushing to view" → MV chain, "AggregatingTransform" → GROUP BY (external aggregation), "FillingRightJoinSide" → JOIN build side, "while reading column" in `part_log` → merge. P-10, P-11, P-12. |
| 5.4 | `system.errors` | `CANNOT_ALLOCATE_MEMORY` (173) or "Too many open files" (76/107) | **critical** | OS-level exhaustion: check `vm.overcommit_memory`, `vm.max_map_count`, `LimitNOFILE` in `host_info.clickhouse_relevant_tunables`. P-13. |
| 5.5 | `host_info.cpu.load_avg_1_5_15` vs `logical_cpus` | load > 2× CPUs *(guideline)* | warning | CPU saturation at collection time. Check concurrent queries (`query_log_details` count per hour), merge pool (2.7), other processes (`top_processes_by_rss`). |
| 5.6 | `host_info.memory` | `swap_total_bytes - swap_free_bytes > 0` | info→warning | Swapping hurts latency unpredictably; ClickHouse expects no swap. |
| 5.7 | `host_info.top_processes_by_rss` | any non-ClickHouse process with RSS > 10 % of RAM *(guideline)* | info | Co-located workloads compete for memory/page cache. |
| 5.8 | `query_log_details` | hourly `count` growing ×3 vs the 7-day baseline while `memory_usage/count` is flat | info | Memory floor rises because of traffic, not a leak — name the traffic vector (user/table/hash) rather than the allocator. P-12. |
| 5.9 | `query_views_log_3_days` | `median_peak_memory_usage` for one view stepping up ≥ 3× across a day boundary while `written_rows` stays flat *(guideline)* | warning | Per-MV memory regression, typically after an upgrade. `sum` alone will not show it — that tracks insert volume. Check `system.version` and P-54. |

## HC-6 Query workload

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 6.1 | `query_log_details` | `exception_code != 0` share > 5 % of `count` *(guideline)*; any single code > 50/h (alert `high_exception_rate`) | warning | Rank codes; resolve names via `error-codes.md`; split `ExceptionBeforeStart` (client/schema/access) vs `ExceptionWhileProcessing` (runtime). |
| 6.2 | `query_log_details` | 202 (`TOO_MANY_SIMULTANEOUS_QUERIES`) > 10/h | warning | `max_concurrent_queries` (default 100 OSS) reached — burst, runaway pool, or slow queries piling up. P-20. Note: in bundles from tool versions before September 2026 the `too_many_simultaneous_queries` alert filtered 252 (TOO_MANY_PARTS) — check the code, not the alert name. |
| 6.3 | `query_log_details` `Select`, one `tables` value | patterns with avg duration > 10 s or `read_bytes/count` > 10 GiB *(guideline)*; top-20 by `query_duration_ms/count` | info→warning | Candidates for `--normalized-query-hash` analysis. Look at `query` sample: `SELECT *`, `FINAL`, no filter on ORDER BY prefix, `LIKE '%…%'`, `JOIN` big right side, `uniqExact`/`COUNT DISTINCT`. P-21, P-50…P-55. |
| 6.4 | `query_log_details` | 159 (`TIMEOUT_EXCEEDED`) or 394 (cancelled) clustered in hours | warning | Slow queries hitting `max_execution_time` / client timeouts; check whether the same hours show 241, high merge activity or Keeper errors — contention, not the query alone. |
| 6.5 | `query_log_details` by `user` | one non-internal user > 70 % of `count` or of `read_bytes` *(guideline)* | info | Workload concentration; useful for the follow-up prompts. |
| 6.6 | `query_log_details` | `query_kind = 'Insert'` count per hour ≫ 1000 with `written_rows/count < 1000` *(guideline)* | warning | Same finding as HC-2.3 seen from the client side. |
| 6.7 | `query_analysis/profile_events_compare` | large `delta` in `SelectedParts`/`SelectedMarks`/`ReadBufferFromS3*`/`OSCPUWait*`/`MemoryTracker*` | info | Explains slow vs fast: pruning lost, cold cache, CPU wait, memory. `text_log_parts`: "Selected N/N parts" = no pruning. |

## HC-7 Inserts

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 7.1 | `asynchronous_insert_log_7_days` | `status != 'Ok'` flushes > 0 | warning | `ParsingError` = bad client data; `FlushError` = flush failed (with `wait_for_async_insert = 0` the client never learned). P-33. |
| 7.2 | `asynchronous_insert_log_7_days` | `p90_flush_ms > 5000` *(guideline)* | warning | Flush latency — MVs on the target, `async_insert_max_data_size` too large, or merge pressure. |
| 7.3 | `query_log_details` `Insert` | `distinct_exceptions` small with high count for 252/241/242 | warning | One recurring insert failure; the message names the table. |
| 7.4 | `system.tables` | ≥ 5 `MaterializedView`s on one source table; MV chains ≥ 2 hops *(guideline)* | info | Every insert block is processed by each MV synchronously; TOO_MANY_PARTS on an MV target means the *source* gets too many small inserts. P-34. |
| 7.5 | `text_log` / `system.errors` | `INSERT_WAS_DEDUPLICATED` (389) or "Deduplication path already exists" | info→warning | Client retries are being deduplicated (normal) — or every insert is (token misuse) → P-33. |
| 7.6 | `query_views_log_3_days` | any `status = 'ExceptionWhileProcessing'` | warning→critical | The MV threw after the base part was committed: the source has the rows, the target does not. `exception_code` names the class (60 after a swap/rename/detach, 241 memory, 252 parts, 341 during drain). **A failed INSERT marks every view in the pipeline with the same message** — attribute it to the view named in the text. P-36. |
| 7.7 | `query_views_log_3_days` | `zero_write_executions / executions > 0.9` on a view with `read_rows > 0` *(guideline)* | warning | The MV ran clean and produced nothing. Legitimate for a filtering MV — compare against that view's earlier buckets and its siblings before concluding. P-35. |
| 7.8 | `query_views_log_3_days` | one view's `written_rows` per hour diverging from a sibling on the same source | warning | One hop of a chain stopped keeping up. Pair with `system.tables` dependencies. P-35. |

## HC-8 Mutations and TTL

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 8.1 | `system.mutations` | age > 3 h with `parts_to_do > 0` (alert `mutation_running_too_long`); `is_killed = 1` still present (cloud) | warning | Slow, blocked or head-of-line: compare `parts_to_do_names` with parts in `system.merges`/`part_log` failures (a merge pinning the part), and `command` (heavy `MODIFY COLUMN`, `UPDATE`/`DELETE` across the whole table). P-14. |
| 8.2 | `system.mutations` | > 100 mutations on one table *(guideline)*; near 1000 = `number_of_mutations_to_throw` | warning→critical | Usually one failing mutation retried, or `DELETE`/`UPDATE` used as a row-level operation. P-15. |
| 8.3 | `part_log` | `event_type = 'MutatePart' AND error != 0` | warning | Read `exception`: 53/70/117/349 = type conversion failure (NULL → non-Nullable, bad JSON hint) → the mutation will never finish; kill and fix the type. P-15, P-32. |
| 8.4 | `part_log` | no `merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')` events over 7 days on tables whose DDL has `TTL` *(from `system.tables.create_table_query`)* | info | TTL not being applied (parts already expired? `merge_with_ttl_timeout` default 4 h; `ttl_only_drop_parts`). Check `system.parts.delete_ttl_info_min/max` = `1970-…` on new parts → TTL not registered. P-18. |
| 8.5 | `system.tables.engine_full` | TTL-heavy tables without `ttl_only_drop_parts = 1` *(guideline)* | info | Whole-part drops are far cheaper than rewriting rows; recommend when TTL aligns with partitions. |

## HC-9 Dictionaries

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 9.1 | `system.dictionaries` | `status IN ('FAILED','FAILED_AND_RELOADING')` or `last_exception != ''` | warning (dashboard red) | Source unreachable/credentials/schema; `dictGet` on it throws 156/36. |
| 9.2 | `system.dictionaries` | `status = 'LOADING'` at collection time | info | Long load — check `loading_duration`, `element_count`. |
| 9.3 | `system.dictionaries` | `bytes_allocated` > 10 % of RAM in total *(guideline)*; `hit_rate < 0.5` on `cache` layouts | info | Memory budget consumer; cache-layout dictionaries are "potentially poor performance". |

## HC-10 Configuration and host tunables (onprem)

Mirrors `hostChecks()` in `internal/dashboard/generator.go`; only the first three are things ClickHouse itself warns about at startup.

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 10.1 | `clickhouse_relevant_tunables.transparent_hugepages` | contains `always` | warning | Startup warning; set `madvise`/`never`. Causes latency spikes and RSS bloat. |
| 10.2 | `vm_overcommit_memory` | `2` | warning | Startup warning; allocations fail early (173). Use `0`. |
| 10.3 | `memory.available_bytes` | `< 2 GiB` | warning | Startup warning; see HC-5.2. |
| 10.4 | `cgroup_memory_limit_bytes` | present and `< memory.total_bytes` | info→warning | Container limit is the real ceiling; ClickHouse ≥ 22.x respects cgroups for `max_server_memory_usage_to_ram_ratio`, older versions may not — compare with `metric_log` memory. |
| 10.5 | `clickhouse_open_files_soft/hard` | `< 500000` *(guideline; packages set 500000)* | warning | "Too many open files" under many parts/connections (76/107). |
| 10.6 | `vm_max_map_count` | `< 262144` *(guideline)* | info | ClickHouse errors when live mappings exceed 90 % of it. |
| 10.7 | `fs_nr_open`, `fs_file_max`, `cgroup_cpu_max`, `vm_swappiness`, THP defrag | — | info | Report as facts; ClickHouse does not document targets. |
| 10.8 | `system.settings` / `system.server_settings` (`changed = 1`), then `configuration/` | `max_server_memory_usage_to_ram_ratio` > 0.9, `max_concurrent_queries` lowered, `background_pool_size` raised without CPU, `mark_cache_size` > 25 % RAM, `max_memory_usage` per profile > server limit, `parts_to_throw_insert` raised into the tens of thousands *(guideline)* | info→warning | Flag deviations from defaults and explain the trade-off; verify each setting exists on this version before commenting (`clickhouse-source.md` §2). |
| 10.9 | `configuration/` | `<text_log>`/`<query_log>`/`<trace_log>` without `<ttl>`/`<partition_by>`, or `text_log` at `trace` level in production | info | Ties to HC-4.4. |
| 10.10 | `configuration/` | `<listen_host>::</listen_host>` or `0.0.0.0` with `users.d` `<networks>` unrestricted; default user without password | warning | Security exposure — report neutrally. |

## HC-11 Logs

| # | Source | Rule | Severity | Meaning / next |
|---|---|---|---|---|
| 11.1 | `system.text_log`, `logs/*.err.log` | `Code: NNN` histogram top-5; any `<Fatal>` | info→critical | Map codes with `error-codes.md`; a Fatal line = HC-1.1. |
| 11.2 | `logs/` | the same message repeating > 100× in a minute | warning | A retry loop (merge failing, fetch failing, dictionary reload) — count it, don't read it; the message names the object. |
| 11.3 | `logs/clickhouse-server.log` startup block | `Available memory … too low`, `Transparent hugepages`, `overcommit`, `TaskStats`/`NETLINK`, `max_map_count`, "Effective user of the process" | info | ClickHouse's own environment warnings; corroborate HC-10. |
| 11.5 | `system.text_log_histogram_1_day` | Error/Fatal `count` per hour ×10 vs the quiet hours; a `logger_class` that appears only in the incident hours | info→warning | The one-day timeline of the server's own words, per component: it tells *when* and *what kind*, then `text_log`/`logs/` tell the exact message. Typical incident classes: `ZooKeeperClient`, `DDLWorker`, `DatabaseReplicated`, `InterserverIOHTTPHandler`, `MergeTreeBackgroundExecutor`, `executeQuery`, `TCPHandler`, `DiskS3`/`*::MetaInKeeper`. |
| 11.4 | `text_log` | `min(event_time)` within minutes of `max(event_time)` | n/a for absence claims | 2000-row cap reached — the slice covers minutes, not a day; never conclude "no errors in the last 24 h" from it. |

## Producing the scorecard

One row per area (HC-1…HC-11), status = worst check in the area, evidence = the single most specific number (`system.parts: db.t partition 202609 has 812 active parts`). Then list findings ordered by severity, each pointing to the check id and pattern id. Areas with all inputs missing are marked *not collected* with the reason (gov, version, disabled table) and the re-collection command.
