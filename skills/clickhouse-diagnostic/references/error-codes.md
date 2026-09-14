# ClickHouse error codes — what they usually mean in a bundle and where to look

Names were taken from `src/Common/ErrorCodes.cpp` on the ClickHouse master branch (2026-09). Codes never change meaning across versions, but a few names were added recently — if a code is missing here, resolve it with `SELECT errorCodeToName(N)` on any ClickHouse ≥ 21.x, or read `ErrorCodes.cpp` at the customer's tag (see `clickhouse-source.md`).

Where codes appear in the bundle: `system.errors` (`code`, cumulative `value` since restart), `system.query_log_details_7_days` (`exception_code` per hour bucket), `system.part_log_3_days` (`error` for failed merges/fetches/mutations), `system.text_log` / `logs/*.log` (`Code: NNN` in message text), `query_analysis/failed_*` (`error_type` as `NAME (code)`), `dashboard.html` (`exceptions`, `server_errors`).

`P-nn` refers to entries in `known-patterns.md`; `HC-n` to checks in `health-checks.md`.

## Capacity, memory, limits

| Code | Name | Usually means | Look at | Pattern |
|---|---|---|---|---|
| 241 | MEMORY_LIMIT_EXCEEDED | A query, merge or mutation exceeded `max_memory_usage` / the server total (`max_server_memory_usage`, `*_ratio`). Message says which: "(for query)", "(total)", "while reading column X" during a merge. | `query_log_details` by `normalized_query_hash`; `part_log` `error = 241` (merge OOM); `metric_log.avg_memory_tracking_bytes` vs RAM/cgroup | P-10, P-11, P-12 |
| 173 | CANNOT_ALLOCATE_MEMORY | mmap/malloc failed at OS level — the host itself ran out (RAM, `vm.max_map_count`, `vm.overcommit_memory=2`). Different from 241: the OS, not ClickHouse's tracker, refused. | `host_info.memory`, `clickhouse_relevant_tunables`, `logs/` around the time | P-13 |
| 252 | TOO_MANY_PARTS | Inserts rejected because a partition has more active parts than `parts_to_throw_insert`. Merges are not keeping up with inserts (or are blocked). **Note: tool versions before September 2026 shipped a `too_many_simultaneous_queries` alert that filtered on 252 — in those bundles that alert is really counting TOO_MANY_PARTS.** | `system.parts` per-partition counts; `part_log` NewPart rate vs MergeParts; `system.merges` | P-01..P-05 |
| 202 | TOO_MANY_SIMULTANEOUS_QUERIES | `max_concurrent_queries` (or `_for_user` / `_for_all_users`) hit. | `query_log_details` hourly `count` peaks by `user`; `system.processes` | P-20 |
| 159 | TIMEOUT_EXCEEDED | `max_execution_time` reached, or a distributed/remote read timed out. | slow patterns in `query_log_details`; `hash_by_host` skew in query analysis | P-21 |
| 160 | TOO_SLOW | `min_execution_speed` / `timeout_before_checking_execution_speed` tripped. | same as 159 | |
| 158 / 307 | TOO_MANY_ROWS / TOO_MANY_BYTES | `max_rows_to_read` / `max_bytes_to_read` quota-style limits (often set in a profile). | `configuration/users.d`; `query_log_details.exception` | |
| 396 | TOO_MANY_ROWS_OR_BYTES | `max_result_rows` / `max_result_bytes`. | same | |
| 201 | QUOTA_EXCEEDED | A `CREATE QUOTA` limit fired for the user. | `query_log_details` by `user` | |
| 236 | ABORTED | Operation cancelled — typically a merge/mutation cancelled because of shutdown, `KILL`, or a part that vanished; for queries, cancelled by the client or the server. In `part_log` after a 241 it means "the retried merge was cancelled". | `part_log` `error IN (236, 241)` sequences | P-11 |
| 394 | QUERY_WAS_CANCELLED | Client disconnected or `KILL QUERY`. Often the *client* timed out before the server did. | pair with `query_duration_ms` distribution | |
| 439 | CANNOT_SCHEDULE_TASK | Thread pool exhausted (`max_thread_pool_size`, background pools). Symptom of saturation, not a cause. | `metric_log` pool columns; `host_info.cpu.load_avg` | P-22 |
| 460 | CANNOT_CREATE_TIMER | Timer creation failed because no thread was available — same class as 439; a timestamp anchor, not a root cause. | same | P-22 |
| 128 | TOO_LARGE_ARRAY_SIZE | Array/string size limit (`max_array_size_as_field`, `format_*`) — usually a data problem. | `query_log_details.exception` | |
| 306 | TOO_DEEP_RECURSION | Deeply nested query/expression. | | |
| 1000 | POCO_EXCEPTION | Wrapped Poco error: `No thread available`, socket errors, `Timeout`. Read the message. | `text_log`, `logs/` | P-22 |
| 1001 | STD_EXCEPTION | Wrapped `std::exception` (`std::out_of_range`, `bad_alloc`…). In merges it often points at a data/serialization bug on that version. | `part_log` `error = 1001` + `exception`; version | P-30 |
| 1002 | UNKNOWN_EXCEPTION | Anything else; frequently the *client* side (`Broken pipe`, connection reset). | `query_log_details.exception` | |

## Parts, merges, mutations, storage

| Code | Name | Usually means | Look at | Pattern |
|---|---|---|---|---|
| 243 | NOT_ENOUGH_SPACE | Disk full or below `min_free_disk_*`; merges need free space ≈ size of the result part. | `system.disks.free_pct`; `system.parts` largest parts | HC-Disk |
| 40 | CHECKSUM_DOESNT_MATCH | Corrupted part data (bit rot, bad disk, interrupted write). Part is usually detached as `broken`. | `system.detached_parts.reason`; `logs/` | P-31 |
| 226 | NO_FILE_IN_DATA_PART | A column/mark file missing inside a part — corruption or an interrupted ALTER. | `detached_parts`, `part_log` | P-31 |
| 233 / 232 | BAD_DATA_PART_NAME / NO_SUCH_DATA_PART | Part named in Keeper/metadata is not on disk (or vice-versa) — replication catch-up or manual detach. | `replication_queue.last_exception`, `detached_parts` | P-31 |
| 235 | DUPLICATE_DATA_PART | Same block inserted twice from different replicas / a replica re-attached a part that already exists. | `replication_queue` | |
| 246 | CORRUPTED_DATA | Generic corruption (compressed block, index). | `detached_parts`, `logs/` | P-31 |
| 384 | PART_IS_TEMPORARILY_LOCKED | Part is pinned by a running merge/mutation; DETACH/DROP PART/mutation on it waits. | `system.merges`, `system.mutations.parts_to_do_names` | P-14 |
| 341 | UNFINISHED | `ALTER ... SETTINGS mutations_sync` / `replication_alter_partitions_sync` waited past its timeout; the mutation is still running in the background. | `system.mutations` | P-14 |
| 517 | CANNOT_ASSIGN_ALTER | A new ALTER cannot be queued while an earlier mutation/ALTER on the same table is unfinished. | `system.mutations` age & `parts_to_do` | P-14, P-15 |
| 692 | TOO_MANY_MUTATIONS | > `number_of_mutations_to_throw` (default 1000) unfinished mutations on one table — usually one repeatedly failing mutation being retried. | `system.mutations` count per table, `command` | P-15 |
| 359 | TABLE_SIZE_EXCEEDS_MAX_DROP_SIZE_LIMIT | `DROP`/`TRUNCATE` refused above `max_table_size_to_drop` (50 GB default). | `configuration/`; not a fault | |
| 36 | BAD_ARGUMENTS | Invalid argument/setting combination. In server logs at startup: an invalid config value (e.g. mutation pool settings inconsistent with `background_pool_size`). | `logs/clickhouse-server.err.log` startup, `text_log` | P-16 |
| 53 / 70 | TYPE_MISMATCH / CANNOT_CONVERT_TYPE | Schema drift between an MV/insert and its target, or a failing `MODIFY COLUMN` mutation (NULL → non-Nullable). | `system.mutations.command`; `query_log_details` by `tables` | P-15, P-32 |
| 349 | CANNOT_INSERT_NULL_IN_ORDINARY_COLUMN | Insert/MV writes NULL into a non-Nullable column. | `query_log_details.exception` | P-32 |
| 117 | INCORRECT_DATA | Input data does not match the declared type (JSON path type hints, malformed values) — fails inserts *and* merges if the bad value already landed. | `part_log` `error = 117`; `query_log_details` inserts | P-32 |
| 6 / 27 / 72 / 38 | CANNOT_PARSE_TEXT / CANNOT_PARSE_INPUT_ASSERTION_FAILED / CANNOT_PARSE_NUMBER / CANNOT_PARSE_DATE | Client sent malformed rows (CSV/JSON/TSV). Client-side data problem. | `query_log_details` `exception_code`, `interface`, `user` | |
| 10 | NOT_FOUND_COLUMN_IN_BLOCK | Column expected in a block is absent — MV/target schema mismatch or a bug on that version. | `tables` involved; version | P-32 |

## Replication and Keeper

| Code | Name | Usually means | Look at | Pattern |
|---|---|---|---|---|
| 999 | KEEPER_EXCEPTION | Any ZooKeeper/Keeper error: session expired, connection loss, operation timeout, node exists / no node. The message carries the Keeper code (`Session expired`, `Connection loss`, `Operation timeout`). Bursts = Keeper unavailable, saturated or a network partition. | `metric_log.zk_hw_exceptions` by hour; `system.replicas.is_session_expired`; `replication_queue.last_exception` | P-40, P-41 |
| 319 | UNKNOWN_STATUS_OF_INSERT | "We tried to commit part, but ZooKeeper is unavailable, insert status is unknown, client must retry": the part may or may not have been committed. Only during Keeper unavailability. | `query_log_details` `Insert` rows in the same hours as 999; `metric_log_coordination` hardware exceptions; `part_log` NewPart vs later duplicates | P-40, P-57 |
| 571 | DATABASE_REPLICATION_FAILED | "ZooKeeper session expired or replication stopped, try again": a Replicated *database* could not process its DDL log. Appears on `CREATE`/`DROP` in `Replicated` databases during Keeper loss; the DDL is replayed afterwards. | `distributed_ddl_queue` (status, exception_code), `system.databases` engine counts, `text_log_histogram` `DDLWorker`/`DatabaseReplicated` | P-57 |
| 221 | NO_SUCH_INTERSERVER_IO_ENDPOINT | A replica asked this server for an interserver endpoint (part fetch, or `SharedMergeTreePartsUpdate:/…/virtual_parts/<replica>` on shared-storage clusters) that is not registered — the table is still loading, was just (re)created, or the server is restarting. Bursts of tens of thousands right after a restart are normal recovery noise; sustained = a table that never came back. | `system.errors` value vs `Uptime`; `logs/*.err.log` `InterserverIOHTTPHandler`; `text_log_histogram` | P-57 |
| 86 | RECEIVED_ERROR_FROM_REMOTE_IO_SERVER | The *other* side of 221: this server called a peer's interserver endpoint and got an HTTP error (500 with the peer's 221 in the body). Same causes, seen from the caller. | `system.errors.last_error_message` names the peer URL and endpoint | P-57 |
| 225 | NO_ZOOKEEPER | Server started without Keeper config, or Keeper config missing for a replicated table. | `configuration/` `<zookeeper>`/`<keeper_server>` | P-42 |
| 242 | TABLE_IS_READ_ONLY | Replica lost its Keeper session / metadata mismatch / disk full; inserts refused until it recovers. | `system.replicas.is_readonly`, `is_session_expired`; `system.disks` | P-40 |
| 164 | READONLY | User/profile is `readonly=1`, or the *server* is in read-only mode (out of disk). Not the same as 242. | `query_log_details` by `user`; `configuration/users.d` | |
| 244 / 308 | UNEXPECTED_ZOOKEEPER_ERROR / UNEXPECTED_NODE_IN_ZOOKEEPER | Keeper metadata inconsistent with local state (leftover znodes, restore onto an existing path). | `replication_queue.last_exception` | P-43 |
| 253 | REPLICA_ALREADY_EXISTS | `CREATE TABLE` on a `Replicated*` path that already has this replica name — stale metadata in Keeper after DROP/re-create. | `logs/` DDL errors | P-43 |
| 224 | REPLICA_IS_ALREADY_ACTIVE | Two servers claim the same replica name (config `<macros>` duplicated). | `configuration/` macros | P-42 |
| 285 | TOO_FEW_LIVE_REPLICAS | Quorum insert (`insert_quorum`) cannot reach enough active replicas. | `system.replicas.active_replicas/total_replicas` | |
| 286 / 319 | UNSATISFIED_QUORUM_FOR_PREVIOUS_WRITE / UNKNOWN_STATUS_OF_INSERT | Quorum insert edge cases; 319 = the client cannot know whether the insert landed (retry safely only with dedup). | | |
| 389 | INSERT_WAS_DEDUPLICATED | Block hash already seen — the insert was silently skipped (normal after client retries; a problem when *every* insert dedups). | `text_log` "Deduplication path already exists"; `query_log_details` `written_rows = 0` | P-33 |
| 741 | TABLE_UUID_MISMATCH | Table UUID resolved at analysis time changed before execution (EXCHANGE/refreshable MV swap race). Not Keeper. | `text_log`; refreshable MVs in `system.tables` | |
| 529 | NOT_A_LEADER | Operation sent to a non-leader replica in a topology that expects a leader (old versions). | | |

## Query, schema, access

| Code | Name | Usually means | Look at | Pattern |
|---|---|---|---|---|
| 47 | UNKNOWN_IDENTIFIER | Column/alias does not exist — client bug or schema change. | `query_log_details` `query` sample, `type = 'ExceptionBeforeStart'` | |
| 60 / 81 | UNKNOWN_TABLE / UNKNOWN_DATABASE | Object missing — dropped/renamed, wrong database, or a race during EXCHANGE. Note: these rows have `tables = ''` in `query_log_details`. | same | |
| 57 | TABLE_ALREADY_EXISTS | Duplicate `CREATE TABLE` without `IF NOT EXISTS`; harmless unless it loops. Variant: "Mapping for table with UUID=… already exists. It happened due to UUID collision" on `CREATE OR REPLACE TABLE db.`.tmp.inner_id.<uuid>`` with a `/* ddl_entry=query-N */` comment = a Replicated database **replaying** materialized-view DDL after a Keeper session loss and colliding with its own first attempt. | `distributed_ddl_queue` (same query under several entries), `query_log_details` `Create` rows with empty `user` | P-57 |
| 84 | DIRECTORY_ALREADY_EXISTS | A merge/fetch tried to create a part directory that is already there — a retry after a half-committed operation (typical right after a Keeper session loss). Harmless when it stops; count it. | `part_log` `MergeParts`/`DownloadPart` with `error = 84` in the incident hours | P-57 |
| 504 | FILE_ALREADY_EXISTS | Same family as 84 for object storage ("Object data/<uuid>/<part>/ already exists: While executing operation #N"): the retried commit found the blob prefix already written. | `part_log` `error = 504`, `blob_storage_log` Upload rows | P-57 |
| 107 | FILE_DOESNT_EXIST | A part file (`primary.idx`, `<col>.cmrk2`, `data.bin`) the metadata says exists cannot be read — "attempt to read data part X (state Active) failed … It can mean that some retryable error happened, or data is corrupted". On object-storage disks it means the metadata (local `.txt` or **Keeper-held**) points at a blob that is gone or was never written; `The specified key does not exist` (S3 `NoSuchKey`) in the same message is the same thing one layer down. | `query_log_details` 107 by table; `blob_storage_log` Delete/Upload for the hour; `system.metrics` `MetadataFromKeeperCacheObjects` per replica; `part_log` 84/504 earlier | P-58 |
| 735 | QUERY_WAS_CANCELLED_BY_CLIENT | The client sent a Cancel packet (timeout on its side, user interrupt, connection pool recycling). Not a server fault by itself; a burst during an incident means clients gave up waiting on stuck inserts/DDL. | `query_log_details` by `user` and hour | P-21 |
| 62 / 80 | SYNTAX_ERROR / INCORRECT_QUERY | Client sent an invalid statement. | by `user`, `interface` | |
| 43 / 44 / 46 | ILLEGAL_TYPE_OF_ARGUMENT / ILLEGAL_COLUMN / UNKNOWN_FUNCTION | Query bug or version mismatch (function missing on this version). | version, `query` sample | |
| 48 | NOT_IMPLEMENTED | Feature not supported (engine, format, on this version / this deployment). | version; engine in `system.tables` | |
| 115 / 452 / 472 | UNKNOWN_SETTING / SETTING_CONSTRAINT_VIOLATION / READONLY_SETTING | Client sets a setting that does not exist on this version, or violates a profile constraint. | `configuration/users.d` constraints | |
| 184 | ILLEGAL_AGGREGATION | Nested aggregate function / aggregate in WHERE. | | |
| 277 | INDEX_NOT_USED | `force_index_by_date` / `force_primary_key` set and the query did not use the index — usually a deliberate guard rail. | `configuration/users.d` | |
| 344 | SUPPORT_IS_DISABLED | Feature disabled by setting (experimental flags, `allow_*`). | | |
| 497 | ACCESS_DENIED | Missing grant. **The collector itself needs `SELECT ON system.*`, `SHOW …`, and in cloud mode `REMOTE` + `CREATE TEMPORARY TABLE`** — if these appear in the tool's own output, the bundle is incomplete. Note: tool versions before September 2026 had `too_many_parts` alert text citing "code 497" — a typo; TOO_MANY_PARTS is 252. | `query_log_details` by `user`; `configuration/users.d` | |
| 516 / 193 / 192 / 195 | AUTHENTICATION_FAILED / WRONG_PASSWORD / UNKNOWN_USER / IP_ADDRESS_NOT_ALLOWED | Client credential / network ACL problems; spikes usually mean a misconfigured client or a scanner. | `query_log_details` `user`, `interface`; `logs/` | |
| 511 | UNKNOWN_ROLE | Role referenced in a grant/profile is missing. | `configuration/users.d` | |
| 507 | UNABLE_TO_SKIP_UNUSED_SHARDS | `force_optimize_skip_unused_shards` set and the query cannot prune shards. | | |
| 519 | NO_REMOTE_SHARD_AVAILABLE | Distributed query found no live replica for a shard. | `system.clusters.errors_count` | P-44 |
| 279 | ALL_CONNECTION_TRIES_FAILED | Distributed/remote query could not reach any replica of a shard. | `system.clusters` | P-44 |
| 574 | DISTRIBUTED_TOO_MANY_PENDING_BYTES | Async `Distributed` insert backlog exceeded `bytes_to_throw_insert`. | | P-44 |
| 156 / 36 | DICTIONARIES_WAS_NOT_LOADED (+ source errors) | Dictionary failed to load: source unreachable, bad credentials, schema mismatch. | `system.dictionaries.status/last_exception` | HC-Dict |

## Network, files, OS

| Code | Name | Usually means | Look at | Pattern |
|---|---|---|---|---|
| 209 / 210 | SOCKET_TIMEOUT / NETWORK_ERROR | Timeouts or resets between client↔server or server↔replica/S3. In `part_log` fetches (`DownloadPart`) = interserver problems. | `part_log` `event_type = 'DownloadPart' AND error != 0`; `text_log` | P-44 |
| 198 | DNS_ERROR | Hostname resolution failed (remote servers, Keeper, S3 endpoint). | `configuration/` hostnames; `logs/` | P-44 |
| 203 | NO_FREE_CONNECTION | Connection pool exhausted (distributed, S3). | | |
| 95 / 96 / 33 / 32 | CANNOT_READ_FROM_SOCKET / CANNOT_WRITE_TO_SOCKET / CANNOT_READ_ALL_DATA / ATTEMPT_TO_READ_AFTER_EOF | Peer closed the connection mid-stream (client disconnects, LB idle timeouts, Keeper hiccups). | `query_log_details` `interface`; `text_log` | |
| 499 | S3_ERROR | Object storage error: throttling (SlowDown/503), AccessDenied, missing object, credentials. Bursts on S3-backed disks stall merges and reads. | `text_log`/`logs/` message text; `part_log` errors; `system.disks.type` | P-45 |
| 76 / 107 / 74 | CANNOT_OPEN_FILE / FILE_DOESNT_EXIST / CANNOT_READ_FROM_FILE_DESCRIPTOR | Filesystem problems: permissions, deleted files, `LimitNOFILE` too low ("Too many open files"). | `host_info.clickhouse_relevant_tunables.clickhouse_open_files_*`; `logs/` | P-13 |
| 204 | CANNOT_FSYNC | Disk I/O error on fsync — failing storage. | `logs/`, `dmesg` (ask) | |
| 412 | NETLINK_ERROR | TaskStats/netlink not permitted (containers) — harmless, disables per-thread OS metrics. | `logs/` startup | |
| 139 / 318 | NO_ELEMENTS_IN_CONFIG / INVALID_CONFIG_PARAMETER | Configuration missing or invalid; on startup the server may refuse to load a section. | `configuration/`; `logs/` startup | P-16 |
| 49 | LOGICAL_ERROR | An internal invariant failed — a bug. Frequent repeats on one code path + this version = check the changelog and open issues before anything else. | `text_log`/`logs/` message and stack; version | P-30 |

## Reading tips

- `system.errors.value` is cumulative since the server started. 10 000 occurrences of `MEMORY_LIMIT_EXCEEDED` over 90 days of uptime is a different story from 10 000 in 3 hours — always pair with `uptime`.
- `query_log_details.type` tells you *when* it failed: `ExceptionBeforeStart` (parsing, analysis, access, limits like 202/497/62) vs `ExceptionWhileProcessing` (runtime: 241, 159, 252, 999…).
- `distinct_exceptions` next to `exception_code`: 400 events / 1 distinct message = one recurring fault; 400 / 400 = many different messages sharing a code (e.g. 47 with different column names).
- Client-side codes (1002 with "Broken pipe", 394, 210 on the client) often *follow* a server-side cause (a slow query hit a load-balancer timeout). Look for the server-side code in the same hour first.
