# bundle（诊断包）布局——`clickhouse_diagnostic_*`压缩包里有什么，以及如何读取每个文件

重命名之前的版本把文件夹和压缩包命名为`clickhouse_backup_<ts>`；布局相同，pre-pass会同时读取两者。

打开bundle里的任何文件之前，先读这一节。它描述的是`clickhouse-diagnostic`（本仓库）生成的压缩包。关于*每个文件是做什么用的、先读什么、健康状态是什么样*，请在下方的列级表格之前先阅读`file-guide.md`里对应的条目。collector（采集器）在不断演进：**务必用你正在读取的文件的第一行来核对列名**——下面的表格描述的是`queries.<mode>/`里当前的SQL，较旧的bundle可能带有一套略有不同的列（例如早期的`part_log`文件还带有`part_name`和一个未取别名的`any(exception)`列）。

## 1. 目录树

**在分隔符修复之前于Windows上采集的bundle看起来是空的。**Tar名称按定义是用斜杠分隔的，但那些构建版本写入了主机分隔符，因此每个条目读起来都是`clickhouse_backup_<ts>\system.parts_<ts>.jsonl`。在macOS或Linux上解压后，这是一个*名称包含反斜杠*的文件，而不是目录，因此这里的每个glob都会落空，`dashboard.html`无法从文件夹打开，bundle会被报告为不可读。pre-pass在解压时会修复这个问题，并在其覆盖说明中写明（"packed by a Windows build that wrote backslash path separators"）。它会拒绝已经以这种方式解压过的文件夹：不要就地重命名这些文件——那会修改证据，而且把名称展平会把共享同一名称的文件合并（`config.d\storage.xml`、`users.d\storage.xml`）。改为在原始压缩包上运行它，输出到一个新目录：

```bash
python3 skills/clickhouse-diagnostic/scripts/inspect_bundle.py clickhouse_backup_<ts>.tar.gz --extract-to <new dir>
```

要区分两者，运行`tar tzf <bundle>.tar.gz | head -3`：旧式Windows压缩包的条目里完全不含`/`。


```
clickhouse_diagnostic_YYYYMMDD_HHMMSS/        # .tar.gz 的单一顶层条目
├── system.<table>[_7_days|_3_days|_1_day]_<ts>.jsonl  # 每个采集查询一个文件（JSONEachRow）；后缀是窗口——旧版工具构建的 bundle 把 part_log 命名为 `_7_days`
├── text_log_<ts>.jsonl                        # 仅在 --collect-text-log 时出现（UTC 时间戳，可能相差 1 秒）
├── query_analysis/<name>_<ts>.jsonl           # 仅在 --query-id / --normalized-query-hash 时出现（12 个文件）
├── configuration/…                            # sanitisation（脱敏）后的 XML/YAML，保留源目录结构（gov 中从不出现）
├── host_info.json                             # 默认在 onprem 中采集（gov 中从不出现）
├── logs/*.log                                 # 默认在 onprem 中采集（gov 中从不出现）
├── keeper/<host>_<port>.txt                   # 每个 Keeper 成员的 four-letter command（四字命令）回复（默认在 onprem 和 gov 中采集；gov 对主机名做哈希；cloud 中关闭）
├── dashboard.html                             # 除非 -skip-dashboard 或 gov
├── schema_graph.html                          # 表依赖图，位于 dashboard.html 旁边，从其 Schema 标签页打开（条件相同）
├── execution_log.txt                          # 每个 collector：结果、耗时、字节数、行数；告警；阶段（先读它）
└── alerts_summary.json                        # 仅在告警运行过但 dashboard.html 缺失时出现
clickhouse_diagnostic_<ts>_gov_name_mapping.csv  # 位于文件夹旁边，从不放进压缩包内部（仅 gov）
```

- 文件夹名里的`<ts>`是运行开始时间，采用**collector主机的本地时间**；`text_log_<ts>`和`query_analysis/*_<ts>`在写入时打的是**UTC**时间戳。
- 扩展名取决于`-output-format`：`.jsonl`（默认，`JSONEachRow`）、`.native`或`.tsv`（`TSVWithNamesAndTypes`：第1行是名称，**第2行是类型**）。
- **只有`.jsonl`可以分析。**本技能中的每个recipe和`scripts/inspect_bundle.py`中的每个读取器都只匹配`*.jsonl`；面对`.native`或`.tsv` bundle，它们什么也找不到，只会报告"0 parts, no findings"——这是一种与健康服务器无法区分的虚假"无异常"。因此`inspect_bundle.py`会直接拒绝这样的bundle（退出码2），而不是误读它。请要求用默认的`-output-format jsonl`重新采集；**不要**把计数搪塞成零。
- 空文件是正常且有意义的：0字节的`system.crash_log_*.jsonl`表示没有崩溃；0字节的`system.merges_*.jsonl`表示采集时没有正在进行merge（合并）。

## 2. 取值编码规则（JSONL）

| 规则 | 为什么重要 |
|---|---|
| 64位整数是**带引号的字符串**（`"normalized_query_hash":"3513886418292139323"`、`"count":"1"`、`"bytes_on_disk":"438"`） | 工具固定设置`output_format_json_quote_64bit_integers=1`。用`int()`/`toUInt64()`解析；**绝不要**用JavaScript数字或`jq`算术（超过2^53会静默舍入）。 |
| 8/16/32位整数、浮点数和布尔值是裸值（`"error":0`、`"interface":2`、`"avg_merge_pool_tasks":0.0348`） | |
| 枚举以**名称**形式到达（`"event_type":"NewPart"`、`"type":"QueryFinish"`、`"level":"Warning"`） | 不要期待整数编码。 |
| DateTime在**服务器**时区里是`"YYYY-MM-DD HH:MM:SS"`；`toString()`包装的列形状相同 | 与UTC日志文件关联时，要记得服务器`timezone()`。 |
| 数组是JSON数组；`Map`列（例如`ProfileEvents`）是JSON对象 | |
| 文本列用`leftUTF8(...)`截断，因此每一行都是合法的UTF-8 | 严格解析器（Python`json`）可以安全使用。 |

## 3. 判定模式

| 信号 | cloud | onprem | gov |
|---|---|---|---|
| 存在`dashboard.html` | 是 | 是 | **否**（不提供） |
| `alerts_summary.json` | 仅在`-skip-dashboard`时 | 仅在`-skip-dashboard`时 | **是**（其唯一用途） |
| `system.crash_log`、`system.stack_trace`文件 | 是 | 是 | **缺失** |
| `database`/`table`/`user`/`host_name`值 | 明文 | 明文 | 64位十六进制字符的SHA-256哈希 |
| `system.query_log_details_7_days`含`query`、`exception` | 是 | 是 | **否** |
| `system.text_log.message` | 文本 | 文本 | 64位十六进制哈希（不可读） |
| `part_log`/`query_log`文件中的`hostname`列 | 是（通过`clusterAllReplicas`在replica（副本）上分发采集） | 仅在 ≥ 23.11的服务器上（单主机） | ≥ 23.11（哈希） |
| `host_info.json`、`logs/`、`configuration/` | 默认关闭 | 默认开启 | 从不 |
| `keeper/` | 默认关闭（用`-keeper-mntr on`强制开启） | 默认开启 | 默认开启，主机名哈希 |
| `execution_log.txt`头部`mode:` | `cloud`——在SharedMergeTree集群上**切换过的onprem运行**也如此（随后会有`collection: onprem → cloud …`，并且`host_info.json`/`logs/`/`configuration/`也会出现） | `onprem` | `gov` |

还要先读`system.version_*.jsonl`（`{"version":"25.3.2.39"}`）——本技能中每条依赖版本的论断都以它为依据。

## 4. 采集查询（`queries.<mode>/`）——逐文件

每个文件的用途、首次阅读要点、健康基线、危险信号和陷阱：见`file-guide.md`。本节是列级参考。

窗口占位符：`{from:7d}`/`{to:now}`= 最近7天，除非给出了`-from`/`-to`；`{from:1d}`= 最近24小时。快照文件没有窗口：它们描述采集那一刻的状态。

### 时间点快照

| 文件 | 列（onprem/cloud） | gov差异 | 备注 |
|---|---|---|---|
| `system.version` | `version` | 相同 | |
| `system.clusters` | `cluster, shard_num, shard_weight, replica_num, host_name, host_address, port, is_local, user, default_database, errors_count, slowdowns_count, estimated_recovery_time`（+ `database_shard_name, database_replica_name, name`在 ≥ 23.5；`is_active`仅在较旧的bundle中——它会让服务器查询每个Replicated数据库的replica；见`system.database_replicas`） | 对cluster、host_name、host_address、port、user、default_database做哈希（+ shard/replica/name） | `errors_count != 0`= 某个节点有失败的分布式查询。 |
| `system.tables` | `database, name, uuid, engine, is_temporary, data_paths, metadata_path, metadata_modification_time, dependencies_*, create_table_query, engine_full, as_select, partition_key, sorting_key, primary_key, sampling_key, storage_policy, comment, has_own_data, loading_dependencies_*, loading_dependent_*`（+ `total_rows, total_bytes`；`metadata_version` ≥ 24.2，`parameterized_view_parameters` ≥ 25.4，`target_database, target_table` ≥ 26.6） | `database`、`name`做哈希，另外`uuid, engine, is_temporary, metadata_modification_time, storage_policy, has_own_data, total_rows, total_bytes`以及四个`dependencies_*`/`loading_dependencies_*`数组逐元素哈希（+ `metadata_version` ≥ 24.2，`target_*`在 ≥ 26.6时哈希）。无DDL，无键。 | 完整的DDL和ORDER BY就在这里——是模式设计类finding（发现）的来源。**`create_table_query`/`engine_full`/`as_select`中的凭据读作`'[HIDDEN]'`**：在 ≥ 23.x上由服务器遮蔽，更旧的版本或遗漏的内容由collector的脱敏器处理（AWS key id、`SETTINGS … password = …`）。 |
| `system.columns` | `database, table, name, type, position, default_kind, default_expression, is_in_primary_key, is_in_sorting_key` | 对database/table/name做哈希；无`default_expression`；两个标志保留 | 很大（每列一行）。键标志说明了过滤列是否在键中，无需解析`sorting_key`。 |
| `system.settings` | `name, value, changed, type`（+ `default` ≥ 23.4；cloud始终） | 相同（值**不**哈希——它们是调优旋钮，不是标识符） | collector用户所看到的会话/profile设置。`changed = 1`的行是定制过的；把`value`与`default`比较。 |
| `system.server_settings`（≥ 23.3；cloud root） | `name, value, default, changed, type` | 路径或URL形状的值，以及`default_database`、`default_replica_name`、`interserver_http_host`、`default_profile`、`merge_workload`、`mutation_workload`，被替换为字面量`REMOVED` | `config.xml`级别的设置（`max_server_memory_usage*`、`background_pool_size`、`max_concurrent_queries`、缓存、`keep_free_space_bytes`……）。在 < 23.3的服务器上缺失（onprem/gov中没有根文件；collector位于`23.3.1.0/`）。 |
| `system.merge_tree_settings`（v0.7） | `name, value, changed, type`（四个在22.8上都存在；无`default`） | `storage_policy`/`disk`的值用salt哈希（可join `system.storage_policies`/`system.disks`）；路径形状的值、workload名称以及列/索引列表类设置读作`REMOVED` | 服务器级MergeTree默认值，从一个replica读取：`parts_to_throw_insert`/`parts_to_delay_insert`，以及在SharedMergeTree上的`shared_merge_tree_parts_load_batch_size`（32）和`shared_merge_tree_leader_update_period_seconds`（30 s）。每表覆盖值在`system.tables.engine_full`中。约350行。 |
| `system.parts` | 41列：`partition, name, part_type, active, marks, rows, bytes_on_disk, data_compressed_bytes, data_uncompressed_bytes, secondary_indices_*, marks_bytes, modification_time, remove_time, refcount, min_date, max_date, min_time, max_time, partition_id, min_block_number, max_block_number, level, data_version, primary_key_bytes_in_memory(_allocated), is_frozen, database, table, engine, disk_name, path, hash_of_*, delete_ttl_info_min/max, move_ttl_info.*` | 对name/database/table/`partition`做哈希；丢弃disk_name、path、hash_* | 当前的collector只返回**active的part（数据分片），按从大到小排序，上限50 000行**（`WHERE active = 1 ORDER BY bytes_on_disk DESC LIMIT 50000`）；2026年9月之前的bundle包含inactive的part——为稳妥起见始终过滤`active = 1`。如果文件恰好有50 000行，说明最小的part被截掉了。即使在cloud中也是单replica（part是共享的）。 |
| `system.detached_parts` | `database, table, partition_id, disk, reason, min_block_number, max_block_number, level`（+ `bytes_on_disk, path` ≥ 22.11；+ `modification_time` ≥ 23.11） | 对db/table做哈希；无disk/path | `reason`说明原因（`broken`、`unexpected`、`ignored`、`clone`、`covered-by-broken`……）。较新的bundle不列出`system`/`information_schema`数据库（每张表的`detached/`目录就是一次列目录；数据库过滤在其之前应用）。 |
| `system.disks` | `name, path, free_space, total_space, used_space, free_pct, type`（+ `unreserved_space` ≥ 22.10） | 对name/path做哈希 | 大小是**人类可读字符串**（`"12.34 GiB"`）；`free_pct`是数值。cloud：每个replica一行。 |
| `system.errors` | `name, code, value, last_error_time, last_error_message, last_error_trace`（trace截断到500） | message哈希，trace为空 | **自服务器启动以来的累计计数器**，按`value`取前100。要算速率需要两个bundle或`uptime`。 |
| `system.merges` | `database, table, elapsed, progress, num_parts, result_part_name, is_mutation, total_size_bytes_compressed, total_size_marks, bytes_read_uncompressed, rows_read, bytes_written_uncompressed, rows_written, memory_usage, thread_id, merge_type, merge_algorithm` | 对db/table做哈希 | 为空 = *在那一瞬间*没有正在进行的merge，而不是"merge坏了"。 |
| `system.mutations` | `database, table, mutation_id, command, create_time, parts_to_do_names, parts_to_do`（+ `is_killed`，cloud） | 对db/table/command做哈希 | **包含已完成的mutation（变更）**：服务器每张表最多保留`finished_mutations_to_keep`（默认100）条，因此行数不是待处理数——过滤`parts_to_do > 0`（HC-8.2就是这么做的）。 |
| `system.replicas` | `database, table, is_leader, can_become_leader, is_readonly, is_session_expired, future_parts, parts_to_check, queue_size, inserts_in_queue, merges_in_queue, part_mutations_in_queue, queue_oldest_time, absolute_delay, last_queue_update_exception` ORDER BY `absolute_delay DESC`——较旧的bundle还带有`log_max_index, log_pointer, total_replicas, active_replicas, zookeeper_exception`，较新的bundle把它们放在`system.replicas_problem_tables`中（请求其中任何一项都会从Keeper读取每张表的状态） | 对db/table和异常字符串做哈希（空保持空） | 在没有`Replicated*`表的服务器上文件为空。`last_queue_update_exception`（此处）和`zookeeper_exception`（problem-tables文件）是`is_readonly = 1`背后的原因。**cloud（v0.7）：**增加`hostname`（每张表每个replica一行）、`oldest_part_to_get`、`inserts_oldest_time`；排序为只读 → 过期 → `absolute_delay` → `inserts_in_queue`降序，上限为**每个主机200行**（恰好200行的主机就是被截断了）。在SharedMergeTree上，`inserts_in_queue`/`absolute_delay`统计的是该replica尚未拉取的level-0 part（HC-3.1）。 |
| `system.replicas_problem_tables` | 最多**200张表**的`database, table, is_readonly, is_session_expired, queue_size, absolute_delay, log_max_index, log_pointer, total_replicas, active_replicas, zookeeper_exception`：先取只读/过期/排队/延迟/检查中的，再取一个健康样本（cloud：按`hostname`，每个replica各自挑选） | 对db/table和`zookeeper_exception`做哈希 | `system.replicas`中由Keeper支撑的列，仅对这些表读取（服务器在读取状态之前先按database/table过滤）。HC-3.3、P-43。 |
| `system.database_replicas`（≥ 25.9） | `database, is_readonly, is_session_expired, shard_name, replica_name`（cloud增加`hostname`） | 对database、shard（分片）和replica名称做哈希 | Replicated数据库的replica健康状况——`system.clusters.is_active`的替代品。`max_log_ptr`/`log_ptr`/`total_replicas`/`zookeeper_exception`不采集（每一项都要从Keeper读取每个数据库的状态）。 |
| `system.replicas_answered`（cloud） | `host_name, host_address, pos, answered`——`default`集群每个主机一行 | — | 请求以`skip_unavailable_shards = 1`运行：宕掉的replica会从每个per-replica文件中缺失，而不是让这些文件失败。`answered`按集群位置（`_shard_num`）而非名称把每个主机匹配到回复（pre-pass HC-0）。 |
| `system.replication_queue` | `create_time, database, table, type, replica_name, is_currently_executing, position, num_tries, postpone_reason, last_exception, merge_type` | 对database/table/replica_name/原因做哈希（空保持空）；`num_tries`是普通计数器 | `type` ∈ GET_PART、MERGE_PARTS、MUTATE_PART、ATTACH_PART、DROP_RANGE、ALTER_METADATA……`num_tries`区分慢队列和卡住的队列；在此列集之前采集的bundle中没有`database`和`num_tries`。 |
| `system.processes` | `user, address, elapsed, read_rows, read_bytes, total_rows_approx, memory_usage, query_id, is_cancelled, is_all_data_sent` | 对user/address做哈希 | collector自己的查询通常就在这里。 |
| `system.dictionaries` | `database, name, uuid, status, origin, type, bytes_allocated, query_count, hit_rate, found_rate, element_count, load_factor, source, lifetime_min, lifetime_max, loading_start_time, last_successful_update_time, loading_duration, last_exception, comment` | 对db/name/origin做哈希；无source/last_exception/comment | `status` ∈ LOADED、FAILED、LOADING、NOT_LOADED、FAILED_AND_RELOADING。 |
| `system.view_refreshes`（≥ 23.12） | `database, view, status, last_success_time, next_refresh_time, exception`（500字符；cloud增加`hostName()`并读取每个replica） | `database`/`view`哈希；用`has_exception`代替文本 | 每个可刷新的materialized view（物化视图）一行（`REFRESH EVERY … / AFTER …`）。`status` ∈ Scheduled、Running、Disabled、WaitingForDependencies、RunningOnAnotherReplica。这些视图从不出现在`query_views_log`中；过期的`last_success_time`或非空的`exception`是它们已停止的唯一迹象。23.12以下缺失。 |
| `system.data_skipping_indices` | `database, table, name, type, expr, granularity`（仅定义；在每个replica上相同，因此cloud中只读一次） | 对db/table/name做哈希；无`expr` | 每个索引一行。`type` ∈ minmax、set、bloom_filter、tokenbf_v1、ngrambf_v1……`expr`是被索引的表达式。配合`query_analysis`的`SelectedMarks`与总marks对比，判断索引是否真的在裁剪（P-53）。 |
| `system.crash_log` | `SELECT *`（event_time, signal, thread_id, query_id, trace, trace_full, version, revision, build_id） | 不采集 | 任何一行 = 服务器收到了致命信号。 |
| `system.stack_trace` | `SELECT *`（每个线程一行） | 不采集 | 采集时每个线程正在做什么的快照。 |
| `system.metrics` | `metric, value, description`（cloud：`hostname, metric, value`，每个replica一行） | 相同 | CurrentMetrics快照：`ZooKeeperSession`（0 = 无Keeper会话）、`ZooKeeperWatch/Request`、`ReadonlyReplica`、`ReplicatedFetch/Send`、`S3Requests`、`FilesystemCacheSize`、`MetadataFromKeeperCacheObjects`（对象存储，元数据在Keeper中）、`Background*PoolTask`、`DelayedInserts`、`MemoryTracking`。 |
| `system.events` | `event, value, description`，其中`value > 0`（cloud：`hostname, event, value`） | 相同 | 和`system.errors`一样是**自服务器启动以来的累计值**：`ZooKeeper{User,Hardware,Other}Exceptions`、`S3*RequestsErrors`、`ReadBufferFromS3RequestsErrors`、`ReplicatedPartFailedFetches`、`FailedQuery`、`RejectedInserts`、`DelayedInserts`。除以`Uptime`。 |
| `system.asynchronous_metrics` | `metric, value, description`，丢弃按核的行（`…CPU12`、`…MHz_12`）（cloud：`hostname, metric, value`） | 还会丢弃按对象计的仪表（`DiskUsed_<disk>`、`Network*_<iface>`、`Block*_<dev>`） | `Uptime`（服务器，单位秒——`system.errors`/`events`的分母）、`ReplicasMaxAbsoluteDelay`、`ReplicasSumQueueSize`、`NumberOfTables/Databases`、`MaxPartCountForPartition`、`TotalPartsOfMergeTreeTables`、`FilesystemCacheBytes`、`MemoryResident`。 |
| `system.databases` | `name, engine, uuid` | 名称哈希；无uuid | `Replicated`数据库的数量 = 每个replica上每个数据库一个DDLWorker + 一套Keeper watch（一个Keeper负载因素）。`uuid`与异常消息中的`data/<uuid>/…`路径对应。 |
| `system.storage_policies` | `policy_name, volume_name, volume_priority, disks, max_data_part_size, move_factor, prefer_not_to_merge` | 名称哈希（与`system.disks.name`和`system.tables.storage_policy`使用相同salt，因此三个文件可以join） | 把`disks`与`system.disks.type`（`ObjectStorage`还是`Local`）join，判断表数据是否存放在S3/Azure/GCS上。 |
| `system.zookeeper_connection`（≥ 23.8） | `name, host, port, index, connected_time, session_uptime_elapsed_seconds, is_expired, keeper_api_version, client_id, enabled_feature_flags`（cloud增加`hostname`） | name/host哈希；无port/client_id/flags | 每个Keeper连接一行。`session_uptime_elapsed_seconds` ≪ 服务器`Uptime` = 会话被重新建立（过期 / Keeper leader变更）；`is_expired = 1` = 当前没有会话。cloud：在replica之间比较`host`和会话年龄。 |
| `system.replicated_fetches` | `database, table, elapsed, progress, result_part_name, partition_id, total_size_bytes_compressed, bytes_read_compressed, source_replica_hostname, source_replica_port, to_detached`（≤ 500，最长的在前；cloud增加`hostname`） | db/table/part/partition/source哈希；无port | 为空 = 没有正在拉取的内容（健康，或没有需要追赶的）。大量长时间拉取 = 故障后正在追赶。 |
| `system.distributed_ddl_queue`（7天） | `entry, entry_version, initiator_host, initiator_port, cluster, query (500), query_create_time, host, port, status, exception_code, exception_text (500), query_finish_time, query_duration_ms`（≤ 5000，最新的在前；cloud中只读一次） | hosts/cluster/query/exception哈希；无ports | 每个（entry, host）一行。`status` ∈ Inactive/Active/Finished/Removing/Unknown。`entry` = `query-NNNNNNNNNN`（Keeper序列）。来自Keeper——Keeper不健康时它会变慢或缺失；此时collector的超时表现为code 159。 |

### 窗口聚合

| 文件 | 粒度/列 | gov差异 | 阅读规则 |
|---|---|---|---|
| `system.query_log_details_7_days` | 1小时桶 × `query_kind, tables, database, table, type, user, interface, normalized_query_hash, exception_code`；求和`memory_usage, result_rows, result_bytes, written_bytes, written_rows, read_rows, read_bytes, query_duration_ms`；`count`、`query`（一个样本的500字符）、`minDate, maxDate`、`exception`、`distinct_exceptions` | 无`query`、`exception`、`distinct_exceptions`；标识符哈希；`tables`是`db.table`字符串的哈希 | **`LEFT ARRAY JOIN tables`：一条触及N张表的查询会产生N行，每个求和值在每行上重复。求和前先过滤到一个`tables`值（或`tables = ''`）；绝不要跨行求和。**`type`包含`QueryStart`行（无资源数据）以及`ExceptionBeforeStart`/`ExceptionWhileProcessing`——看性能时过滤`type = 'QueryFinish'`，看失败时过滤`exception_code != 0`。`distinct_exceptions` = 有多少条不同的消息共享该code（40个事件 / 1个distinct = 一个反复出现的故障）。 |
| `system.part_log_3_days`（3天窗口） | 12小时桶（≥ 23.11上为1小时 + `hostname`）× `event_type, merge_reason, partition_id, table_name, error`；`exception, distinct_exceptions`；求和`peak_memory_usage, duration_ms, size_in_bytes`；`count` | 分别对`database`、`table`做哈希；无异常列 | `event_type`名称：NewPart、MergeParts、DownloadPart、RemovePart、MutatePart、MovePart、MergePartsStart、MutatePartStart。`merge_reason`：NotAMerge、RegularMerge、TTLDeleteMerge、TTLRecompressMerge。`error != 0` = 操作失败（code在`error`中）。`duration_ms`/`size_in_bytes`是桶内的求和值——除以`count`得到平均值。 |
| `system.query_views_log_3_days`（3天窗口） | 1小时 × `view_name, view_target, view_type, status, exception_code`；`executions, zero_write_executions`；求和`read_rows, read_bytes, written_rows, written_bytes, view_duration_ms`；`max_view_duration_ms`；`avg/median/max_peak_memory_usage`；`exception`（500字符）；`minDate, maxDate`（≥ 23.11增加`hostname`） | 无`exception`；`view_name`/`view_target`拆分为哈希后的`view_database`/`view_table`和`target_database`/`target_table` | 每个（bucket, view, status）一行。`status` ∈ QueryStart、QueryFinish、ExceptionBeforeStart、ExceptionWhileProcessing。`zero_write_executions`统计`read_rows > 0 AND written_rows = 0`的执行次数——要当作`executions`的比例来读，绝不要读绝对值。**在默认设置下，一次失败的INSERT会让管道中的每个视图都得到一行`ExceptionWhileProcessing`，且消息相同**——一次失败在每个视图里各出现一次；罪魁祸首是文本中点名的视图（"while pushing to view X"），它的行数就是失败次数。在`materialized_views_ignore_errors = 1`下只有抛错的视图会被标记。失败行上的`written_rows`统计的是从未落地的行。不采集`view_uuid`：服务器会往里面写零UUID。未配置`<query_views_log>`时缺失（此时collector报告code 636，而不是60）。 |
| `system.metric_log_7_days` | 1小时桶：`time, avg_merge_pool_tasks, max_merge_pool_tasks, avg_fetch_pool_tasks, max_fetch_pool_tasks, avg_schedule_pool_tasks, max_schedule_pool_tasks, avg_common_pool_tasks, max_common_pool_tasks, max_replica_avg_fetch_pool_tasks, max_replica_avg_schedule_pool_tasks, max_replica_avg_merge_pool_tasks, avg_interserver_connections, zk_transactions, zk_hw_exceptions, avg_memory_tracking_bytes`（`max_/avg_*_schedule/common`和`max_replica_avg_*`列来自v0.7；cloud中`max_replica_avg_*`是最差replica的小时平均值——饱和粒度——而`avg_*`是集群均值） | 相同 | dashboard（仪表盘）把fetch、schedule和merge池的平均值与它们的大小画在一起（Replication部分）；内存、common池和Keeper计数器只在文件里。`avg_memory_tracking_bytes` ≈ 被跟踪的服务器内存；与`host_info.memory.total_bytes`或cgroup限制比较。 |
| `system.asynchronous_insert_log_7_days` | 1小时 × `database, table, status`；`flushes, total_rows (≥ 23.4), total_bytes, avg_flush_ms, p90_flush_ms` | 对db/table做哈希 | 在 < 22.10的服务器上或未使用异步插入时缺失。`status` ∈ Ok、ParsingError、FlushError。 |
| `system.metric_log_coordination_3_days` | 1小时桶；列通过**正则**从`metric_log`中选取，因此依赖版本：ZooKeeper*、Keeper*、S3*、DiskS3*、ReadBufferFromS3*、WriteBufferFromS3*、AzureBlobStorage*、ObjectStorage*、FilesystemCache*、CachedReadBuffer*、ReplicatedPart*、MergeTreeDataWriter*、InsertedParts、FailedQuery/FailedInsertQuery/FailedSelectQuery、Query/InsertQuery/SelectQuery、DelayedInserts、RejectedInserts、DistributedConnectionFail*、Merge、MergedRows、MergesTimeMilliseconds的`sum(ProfileEvent_<X>)`；ZooKeeperSession/Watch/Request、MemoryTracking、Query、ReadonlyReplica、ReplicatedFetch/Send/Checks、S3Requests、FilesystemCacheSize/Elements、Background*PoolTask、DelayedInserts、PartsActive/Outdated/Deleting、TCP/HTTP/InterserverConnection、DistributedFilesToInsert、Merge、Move的`max(CurrentMetric_<Y>)` | 相同 | 列名中字面包含聚合：`"sum(ProfileEvent_ZooKeeperHardwareExceptions)"`。此版本没有的指标就直接缺失。cloud：跨replica求和/取最大。 |
| `system.metric_log_by_host_3_days`（仅cloud，v0.7） | 1小时 × `hostname`：`avg_/max_fetch_pool_tasks, avg_/max_schedule_pool_tasks, avg_/max_merge_pool_tasks, max_parts_active, cpu_us, zk_transactions, zk_hw_exceptions, zk_wait_us`，加上正则选取的`max(CurrentMetric_Background{Fetches,Schedule,MergesAndMutations}PoolSize)`（**实时**池大小）和`sum(ProfileEvent_SharedMergeTree{DataPartsFetchAttempt,DataPartsFetchFromS3,DataPartsFetchFromPeer,SelectPartsForRendezvousFetchParts,SelectPartsForCoordinatedFetchParts,HandleFetchPartsMicroseconds,VirtualPartsUpdatesLeaderSuccessfulElection,VirtualPartsUpdatesLeaderFailedElection,ScheduleDataProcessingJob})` | 不适用（仅cloud） | `metric_log_7_days`的按主机伴随文件。在此版本没有该计数器时正则列缺失，在ReplicatedMergeTree上为零；`cpu_us`是跨线程求和——完整桶的百分比 = `cpu_us / (3 600 × 1e6 × cores) × 100`。hosts × 72行。 |
| `system.text_log_histogram_1_day` | 1小时 × `level` × `logger_class`；`count`、`example`（300字符）（cloud增加`hostname`；≤ 10 000） | `logger_class`保持可读（是组件名）；`<disk>::`前缀被遮蔽为`*::`，仍然包含点号的回退值（无法识别的`db.table`）会被哈希；`example`哈希 | `logger_class`是**归一化到组件**的logger：`db.t (uuid)(PartsUpdateElection)` → `PartsUpdateElection`、`DDLWorker(db)` → `DDLWorker`、`db.t (ReplicatedMergeTreeQueue)` → `ReplicatedMergeTreeQueue`；普通logger保持不变。这个文件告诉你Warning/Error的量在*什么时候*变化、发生在哪个组件。 |
| `system.error_log_7_days`（≥ 24.8） | 1小时 × `code, error, remote`；`errors`（每分钟计数之和；cloud增加`hostname`；≤ 20 000） | 相同 | `system.errors`的历史：每小时来自每个线程的每个code，包括`query_log`从来看不到的后台操作。`remote = 1` = 从另一台服务器收到的。 |
| `system.text_log_keeper_1_day` | 1小时 × `marker` ∈ session_expired, session_finalized, connected, reconnecting, connection_loss, operation_timeout, other_keeper；`count, first_seen, last_seen, example`（200字符；cloud增加`hostname`；≤ 5000） | example哈希 | 所有日志级别，过滤到Keeper客户端行——会话丢失的*分钟*，以及`connected`示例中的Keeper主机。在`text_log`级别高于Information的服务器上为空。 |
| `system.zookeeper_log_errors_1_day`（仅当配置了`<zookeeper_log>`时） | **仅失败的Response行**（`PREWHERE type = 'Response' AND error != 'ZOK'`），1小时 × `op_num` × `error`；`failed_requests`、`sessions_affected`（`uniq(session_id)`）、`max_duration_ms`（cloud增加`hostname`；≤ 20 000） | 相同 | `error` ∈ `ZSESSIONEXPIRED`、`ZCONNECTIONLOSS`、`ZOPERATIONTIMEOUT`（丢失）、`ZNONODE`、`ZNODEEXISTS`、`ZBADVERSION`（普通控制流——计数高是正常的）。成功量和延迟**不**在这里：从`metric_log_coordination`取（`ZooKeeperTransactions`、`ZooKeeperWaitMicroseconds / ZooKeeperTransactions`）；会话抖动从`text_log_keeper_1_day`取（每小时的`connected`）。文件缺失 = 表未启用。 |
| `system.aggregated_zookeeper_log_by_hour_2_days`（≥ 26.3） | 1小时 × `component` × `operation`：`ops`（独立请求）、`in_multi`（Multi/MultiRead内部的子请求）、`avg_latency_us`（加权，仅独立请求）、`worst_group_latency_us`、`errors`（非ZOK code → 计数的映射）、`sessions`；cloud：跨replica求和，外加`replicas`、`busiest_replica`、`busiest_replica_ops`（≤ 50 000） | 相同 | 来自`system.aggregated_zookeeper_log`（自25.9起默认开启，30天TTL）——服务器中**谁**在发送Keeper请求。26.3以下缺失（跳过，而非失败）。 |
| `system.aggregated_zookeeper_log_by_minute_1_day`（≥ 26.3） | 1分钟：`ops`、`in_multi`、`avg_latency_us`、`worst_group_latency_us`、`failed`（非ZOK）、`loss_failures`（连接丢失 / 会话过期 / 超时）、`sessions`；cloud增加`hostname`（每个replica-分钟一行） | 相同 | Keeper **何时**变慢或失败，以及在哪个replica上。 |
| `system.aggregated_zookeeper_log_paths_1_day`（≥ 26.3） | 每个路径类别一行（数字段 → `N`）：`path`、`ops`（含子请求）、`in_multi`、`creates`、`removes`、`net_creates`、`reads`、`avg_latency_us`、`raw_paths`；请求最多的50个类别 ∪ 净创建最多的50个类别；cloud增加`replicas` | `path`替换为`shape`（保留ClickHouse自己的路径词，其余为`*`）+ `path_id`（加盐哈希） | 负载和znode增长**在哪里**。Multi信封（无路径）被排除。 |
| `system.blob_storage_log_7_days`（≥ 23.11，仅当配置了`<blob_storage_log>`时） | 1小时 × `event_type`（Upload、Delete、MultiPartUpload*）× `disk_name` × `failed`；`count`、`bytes`、`example_error`（cloud增加`hostname`；≤ 20 000） | disk/error哈希 | `bytes`不含Delete行（它们的`data_size`是UInt64最大值）。`Delete`小时之后出现`FILE_DOESNT_EXIST`/`NoSuchKey`读取，指向被删除的blob；`failed = 1`的Upload = 从未到达桶的写入。 |
| `system.text_log` | 行，最近**24小时**，`level IN (Warning, Error, Critical, Fatal)`，**LIMIT 2000**，按**严重程度优先**排序（Fatal → Critical → Error → Warning），再按最新，**每个（level, logger）最多200行**：`event_time, level, logger_name, message`（500字符） | `logger_name`和`message`哈希 | 由于采用严重程度优先排序，Warning洪流不再能掩盖Error行；不过，在嘈杂的服务器上2000行可能只覆盖几分钟——在对窗口下任何结论之前，先看最旧的`event_time`。OSS中`text_log`默认禁用；文件缺失通常就是这个原因。 |
| `text_log_<ts>`（可选切片） | 显式`--from/--to`窗口和`-text-log-level`的完整行 | 在gov中拒绝 | 在 ≥ 23.1上包含`message_format_string`——按它分组可合并变体。 |

### `query_analysis/`（仅在使用`--query-id`/`--normalized-query-hash`时；gov中从不）

| 文件 | 回答的问题 |
|---|---|
| `query_details` | 重点执行的完整`query_log`行：耗时、内存、读/写行数、`tables`、`exception`、`query`、`ProfileEvents`映射（+ `query_cache_usage` ≥ 23.8、`peak_threads_usage` ≥ 23.9） |
| `profile_events` | `metric, value`——重点执行的所有ProfileEvents，从大到小 |
| `text_log_parts` | "Selected N/M parts by partition key, K marks…"和"Reading approx. N rows with M streams"这两类行——它是裁剪了还是全扫描？ |
| `text_log_full` | 重点查询的每一行text_log（≤ 5000） |
| `tables_for_query` | 查询触及的每张表的当前DDL和大小 |
| `fast_slow_query_ids` | 窗口内同一`normalized_query_hash`的最慢与最快执行 |
| `profile_events_compare` | `metric, slow_value, fast_value, delta, percentage_diff`——最具诊断价值的文件 |
| `hash_by_host` | 按主机的`executions, max/min/avg/p95 duration, avg/max memory, errors`——是某个节点慢吗？ |
| `hash_summary` | 每分钟的`executions, succeeded, failed` |
| `failed_over_time` | 按`error_type`统计的每分钟失败（"NAME (code)"） |
| `failed_queries` | 按tables × 错误类型 × user统计的失败，附带`sample_exception`、`sample_query` |
| `executions_timeline` | 每次执行一行（≤ 10 000）：`ts, query_id, type, exception_code, query_duration_ms, memory_usage, user_cpu_us, read_rows, read_bytes, hostname, user` |

## 5. `host_info.json`

```
collected_at                      RFC3339 UTC
os        { available, hostname, distro, distro_version, kernel_version, kernel_full, arch, go_os, uptime_seconds }
cpu       { available, logical_cpus, model_name, vendor_id, mhz, load_avg_1_5_15: [s,s,s], notable_flags: [avx2|avx512f|asimd|sve…] }
memory    { available, total_bytes, free_bytes, available_bytes, buffers_bytes, cached_bytes, swap_total_bytes, swap_free_bytes }
disks     [ { device, mount_point, fs_type, total_bytes, free_bytes, used_pct } ]      # 包含 tmpfs/overlay；过滤真实挂载点
top_processes_by_rss [ { pid, ppid, state, rss_bytes, threads, command } ]              # ≤ 25
clickhouse_relevant_tunables { transparent_hugepages, transparent_hugepages_defrag, vm_swappiness, vm_overcommit_memory,
                               vm_max_map_count, fs_nr_open, fs_file_max, cgroup_memory_limit_bytes, cgroup_cpu_max,
                               clickhouse_open_files_soft, clickhouse_open_files_hard, clickhouse_nproc_soft, clickhouse_process }
notes     [ "…what could not be read and why…" ]
```
字段在无法读取时省略；`available: false` + `notes`标记一个降级的部分（典型情况是工具在笔记本电脑上针对远程服务器运行——此时host facts（主机信息）描述的是**错误的机器**；要说明这一点并忽略它们）。

## 6. `logs/`

- `clickhouse-server.log`、`clickhouse-server.err.log`（以及仅在`-logs-include-archives`时的轮转`*.gz`/`*.zst`）。来自多个目录的文件会被展平；名称冲突会加上`-2`、`-3`后缀。
- 超过`-logs-max-mb`（默认50 MiB）的文件会被**从尾部截断**，并以以下内容开头：
  `### support-diagnostic: TRUNCATED — original N bytes, kept last M bytes (tail) ###`
  第一行幸存的行*不是*日志的开头。在推断"它是什么时候开始的"之前，先检查这个头部。
- 行格式：`2026.08.25 12:06:56.858337 [ thread ] {query_id} <Level> Logger: message`。用grep，不要从头到尾读。

## 6a. `keeper/`

每个Keeper成员一个`keeper/<host>_<port>.txt`（v0.7+ 的bundle；在23.8之前的服务器上、cloud中默认、或使用`-keeper-mntr off`时缺失）。成员是`system.zookeeper_connection`（每个已配置连接*所在*的成员——每个连接一行，不是每个ensemble成员一行）与服务器配置中`<zookeeper><node>`条目的并集（当工具在服务器上运行时）；因此远程或cloud运行只包含所连接的成员。纯文本：

```
# clickhouse-diagnostic — Keeper four-letter command（四字命令）
# target: <host or gov hash>:<port>
# ensemble: default | <auxiliary ZooKeeper name, gov: hashed>   # 在较旧的 bundle 中缺失；
#   辅助成员的文件是 <ensemble>__<host>_<port>.txt，因此共享的 host:port 永不冲突
# collected_at: RFC3339 UTC
# commands: ruok, isro, lgif, srvr, mntr, conf, dirs（从不发送 stat/cons——它们会列出客户端地址）
#   Raft/settings 命令之前的 bundle 只带 ruok、srvr、mntr

## ruok
imok
## isro
rw                                                  # ro = 只读成员或 observer
## lgif                                             # 即使成员不在服务中也会应答
first_log_idx  first_log_term  last_log_idx  last_log_term
last_committed_log_idx  leader_committed_log_idx  target_committed_log_idx  last_snapshot_idx
## srvr
ClickHouse Keeper version: v26.7.5.10-stable-…      # Keeper 版本——服务器自身的版本在 system.version
Latency min/avg/max: 0/1/193                        # 毫秒，自该成员启动以来
Received: … / Sent: … / Connections: … / Outstanding: 0
Zxid: 0x77e1
Mode: leader | follower | standalone | observer
Node count: 121
## mntr
zk_version  zk_avg_latency  zk_max_latency  zk_min_latency  zk_packets_received  zk_packets_sent
zk_num_alive_connections  zk_outstanding_requests  zk_server_state  zk_znode_count  zk_watch_count
zk_ephemerals_count  zk_approximate_data_size  zk_key_arena_size  zk_latest_snapshot_size
zk_open_file_descriptor_count  zk_max_file_descriptor_count  zk_followers  zk_synced_followers   # 最后两个只在 leader 上
## conf
key=value per coordination setting: snapshot_distance  reserved_log_items  stale_log_gap
snapshot_transfer_chunk_size  write_snapshot_version  …   (gov: only numbers, booleans and plain words survive;
#   paths、hosts、addresses、*_path、*_disk、dataDir、dataLogDir 和 server.N 读作 REMOVED）
## dirs
snapshot_dir_size: <bytes>
log_dir_size: <bytes>
```
失败的命令在其`##`标题下写为`error: connection refused`/`error: timeout after 10s`；空回复则写成一条关于`four_letter_word_white_list`的说明。被读取截止时间截断的回复会保留，并在后面跟着`error: timeout after 10s — reply truncated`。某个成员首次网络失败之后，其余命令写为`skipped — <command> timed out`。每个成员的状态——`ok`（`ruok`、`srvr`和`mntr`都有应答；较新命令在较旧Keeper上缺失不会降低它）、`not serving`（成员回答`This instance is not currently serving requests`）、`partial`（部分应答）、`refused`、`timeout`、`failed`（所有命令都因其他原因失败，或文件无法写入）——也会以每个成员一行`keeper`的形式出现在`execution_log.txt`中。在gov中，名称和头部里的主机是`hex(SHA256(host ‖ salt))`——与gov `system.zookeeper_connection`文件中`host`的值相同，因此两者可以join——并且任何带有`ip:port`的回复行都被丢弃。只有*本*服务器配置的成员会被探测：目录中缺失的成员不一定宕机，它可能只是不在本服务器的`<zookeeper>`块中。

## 7. `configuration/`

配置目录的镜像（`config.d/…`、`users.d/…`，有时还有`config.xml`）。凭据、密钥、令牌、PEM块、长十六进制/base64数据块都被替换；**主机名、IP、集群拓扑、宏、路径、表名和每一项性能设置都保留**。解析失败的文件被跳过（fail-closed），因此文件缺失并不证明某个设置未设置。

## 8. `dashboard.html`

自包含；所有面板数据都以一个JSON字面量嵌入在以`const DATA = {`开头的那一行。Alert Summary面板会把长内容（堆栈跟踪、第五个之后的实例、规则描述的尾部）折叠在`<details>`开关后面——当你需要每个实例和完整异常文本时，读`DATA.alerts`而不是渲染后的页面。键：`alerts`（完整规则结果，**包括匹配的行**）、`keeper_metric_hourly`（每小时：`transactions, hw_exceptions, user_exceptions, wait_us, sessions_min, sessions_max`——Keeper健康测试的输入，7天；**先检查`keeper_wait_available`/`keeper_session_available`**——当其中任何一个为`false`时，该服务器的版本不导出那个计数器，生成器用`0`替代，这与真实的零无法区分，所以没有它们时，绝不要把`sessions_min = 0`读成会话丢失，也不要把`wait_us = 0`读成零延迟）、`keeper_errors_hourly`（999/242/319/571/252的`time, code_name, count`；`keeper_errors_source`说明它来自`error_log`还是`query_log`）、`keeper_connection`（实时的`zookeeper_connection`行）、`version, uptime, total_databases, total_tables, active_parts, total_size`、`host_info`、`host_checks`、`storage_by_db`、`engines_dist`、`tables_list`、`query_by_time`、`query_by_kind`、`query_slow`、`query_heavy`、`query_by_user`、`exceptions`、`part_log_by_time`、`part_log_by_type`、`dictionaries`、`crash_log`、`mutations`、`detached`、`replication_queue`、`clusters`、`replicas`、`disks`、`text_log`、`bundle_files`、`server_errors`、`high_part_count`、`ttl_activity`、`async_inserts`、`schema_graph`（`file, tables, columns, databases, mvs, refreshable, dictionaries, indices`——仅在写入了`schema_graph.html`时出现），以及运行了查询分析时的`qa_*`。提取方法见`reading-recipes.md`。

## 8b. `schema_graph.html`

schema graph（模式关系图）就是表依赖图，改编自ClickHouse的`/schema`页面，完全由以`const DATA = {`开头的那一行上的一个JSON字面量渲染。键：`tables`（`database, name, engine, engine_full, create_table_query, sorting_key, primary_key, partition_key, sampling_key, total_rows, total_bytes, comment, target_database, target_table, dependencies_*, loading_dependencies_*`——排除系统数据库）、`columns`（`database, table, name, type, is_key, has_default`）、`dictionaries`（`database, name, source`）、`refreshes`（`database, view, status, last_success_time, next_refresh_time, exception`）、`indices`（`database, table, name, type, expr, granularity`）、`ddl_formatted`（当`create_table_query`在服务器端用`formatQueryOrNull`美化打印时为true，每个子句一行）、`has_target`（26.6以下为false：此时MV → target的边从`dependencies_*`推断，会漏掉隐式的`.inner_id.*`表）、`version`、`generated_at`。这里的DDL经过了与JSONL相同的`[HIDDEN]`脱敏器。`dashboard.html`在其Schema标签页第一次点击时把该文件加载到一个frame中；单独打开时效果相同，并显示一个返回链接。两个页面通过`localStorage`遵循同一主题。**pre-pass从不会去抓取它**——要同样的数据事实，读`system.tables_*.jsonl`。

## 8a. `execution_log.txt`

纯文本，在打包之前写入。头部（`started`、`finished`、`mode`——collector实际运行的模式，`collect`——**essential或full**，以及原因（负载探测的原因、`requested with -collect …`，或`started full, switched to essential: <query> timed out …`），外加应用的essential设置；应用了时间预算时有`budget`；未运行的collector状态为`skipped`并附原因（essential采集、时间预算、无Keeper会话），`server`、`target`、`node`——应答服务器的`hostName()`（`FQDN()`），`uptime`/`uptime-seconds`——该节点已运行多久，`collection`——当运行在SharedMergeTree集群上切换了`onprem → cloud`，或停留在单个节点上及其原因时出现，`warnings`——运行时打印的负载均衡器/混合主机行，每行带`Warning:`（削弱bundle的可信度）或`Note:`（预期内，例如Cloud服务的负载均衡端点）前缀，`window`、`timeout`、`format`），一个**Summary**（`collectors: N ok, M failed, K empty — T of query time`、告警计数、各阶段耗时），**Most expensive collectors**（按耗时取前10，附字节数和行数），**Failed collectors**附ClickHouse错误文本（上限300字符），然后是**All entries**管道表——`| # | stage | name | source | status | duration_ms | bytes | rows | note | error |`——其中`stage` ∈ collector / alert / text_log / analysis，`source`是版本目录（`root` = 基础文件），`note`携带输出文件名或告警实例数。**先读它**：缺失的结果文件是一个`failed`的collector（它的错误在这里），而不是空表；code为159的collector撞上了工具自己的`-query-timeout`；在小服务器上耗时数十秒的collector值得缩短窗口。此文件出现之前的工具构建所产出的bundle没有它。

## 9. `alerts_summary.json`

```json
{ "evaluated": 8, "fired": 1, "errored": 2, "not_applicable": 1, "note": "...",
  "rules": [ { "name": "too_many_parts", "title": "...", "severity": "warning", "file": "too_many_parts.yaml",
               "state": "fired|clean|skipped|error", "instance_count": 12,
               "skip_reason": "...", "error": "...", "checked_at": "..." } ] }
```
匹配的行**从不**包含在内——`instance_count`是替代物。`evaluated`不含被跳过和出错的规则。`error`状态*不是* finding（规则无法运行：此版本缺列、缺少grant（授权））；`skipped`状态表示这里不存在该系统表。

规则阈值在`health-checks.md` §1中，因此没有summary时你可以从JSONL自行重新评估。

## 10. gov模式的具体细节

- 每个`database`、`table`、`name`、`user`、类主机名的值都是`hex(SHA256(value || salt))`。哈希**在同一个salt内是稳定的**，因此你仍然可以用它们join文件并按表计数。
- 映射CSV（`database,table,database_hash,table_hash`）只存在于collector的机器上。如果用户有它，主动提出**在本地**解析哈希；绝不要索要salt。
- gov中不可用：dashboard及其schema graph、查询分析、查询文本、异常文本、配置、host facts、日志、`--collect-text-log`、`crash_log`、`stack_trace`。`keeper/`**会**被采集（仅计数器；成员主机像其他所有标识符一样哈希）。`system.tables`中哈希后的`dependencies_*`数组仍可让你按来源统计MV并沿哈希追踪链条。要说明哪些finding因此无法得到，而不是猜测。