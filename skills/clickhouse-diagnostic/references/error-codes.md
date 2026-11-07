# ClickHouse错误码——在bundle（诊断包）中通常意味着什么、该看哪里

名称取自ClickHouse master分支的`src/Common/ErrorCodes.cpp`（2026-09）。错误码的含义从不随版本变化，但有少数名称是近期新增的——如果这里缺少某个错误码，可在任何ClickHouse ≥ 21.x上用`SELECT errorCodeToName(N)`解析，或读取客户tag处的`ErrorCodes.cpp`（见`clickhouse-source.md`）。

错误码在bundle中出现的位置：`system.errors`（`code`，自重启以来的累计`value`）、`system.query_log_details_7_days`（每个小时桶的`exception_code`）、`system.part_log_3_days`（失败的merge/fetch/mutation的`error`）、`system.text_log` / `logs/*.log`（消息文本中的`Code: NNN`）、`query_analysis/failed_*`（`error_type`，格式为`NAME (code)`）、`dashboard.html`（`exceptions`、`server_errors`）。

`P-nn`指`known-patterns.md`中的条目；`HC-n`指`health-checks.md`中的检查项。

## 容量、内存、限制

| 错误码 | 名称 | 通常意味着 | 查看 | 模式 |
|---|---|---|---|---|
| 241 | MEMORY_LIMIT_EXCEEDED | 查询、merge或mutation超出了`max_memory_usage` / 服务器总量（`max_server_memory_usage`、`*_ratio`）。消息会说明是哪一种："(for query)"、"(total)"、merge过程中的 "while reading column X"。 | 按`normalized_query_hash`查`query_log_details`；`part_log`中`error = 241`（merge OOM）；`metric_log.avg_memory_tracking_bytes`对比RAM/cgroup | P-10, P-11, P-12 |
| 173 | CANNOT_ALLOCATE_MEMORY | 操作系统层面的mmap/malloc失败——主机本身耗尽（RAM、`vm.max_map_count`、`vm.overcommit_memory=2`）。与241不同：拒绝的是操作系统，而不是ClickHouse的内存追踪器。 | `host_info.memory`、`clickhouse_relevant_tunables`、事发前后的`logs/` | P-13 |
| 252 | TOO_MANY_PARTS | 某个分区的active part数量超过`parts_to_throw_insert`，插入被拒绝。merge跟不上插入（或被阻塞）。**注意：2026年9月之前的工具版本带有一条`too_many_simultaneous_queries` alert（告警规则），实际过滤的是252——在这些bundle中，该alert真正统计的是TOO_MANY_PARTS。** | 按分区统计的`system.parts`；`part_log`的NewPart速率vs MergeParts；`system.merges` | P-01..P-05 |
| 202 | TOO_MANY_SIMULTANEOUS_QUERIES | 触达`max_concurrent_queries`（或`_for_user` / `_for_all_users`）。 | 按`user`查看`query_log_details`每小时的`count`峰值；`system.processes` | P-20 |
| 159 | TIMEOUT_EXCEEDED | 达到`max_execution_time`，或分布式/远程读取超时。 | `query_log_details`中的慢查询模式；查询分析中`hash_by_host`的倾斜 | P-21 |
| 160 | TOO_SLOW | 触发了`min_execution_speed` / `timeout_before_checking_execution_speed`。 | 同159 | |
| 158 / 307 | TOO_MANY_ROWS / TOO_MANY_BYTES | `max_rows_to_read` / `max_bytes_to_read`这类配额限制（常在profile中设置）。 | `configuration/users.d`；`query_log_details.exception` | |
| 396 | TOO_MANY_ROWS_OR_BYTES | `max_result_rows` / `max_result_bytes`。 | 同上 | |
| 201 | QUOTA_EXCEEDED | 该用户的某条`CREATE QUOTA`限制被触发。 | 按`user`查`query_log_details` | |
| 236 | ABORTED | 操作被取消——通常是merge/mutation因关闭、`KILL`或part消失而被取消；对查询而言，是被客户端或服务器取消。在`part_log`中出现在241之后时，意思是"重试的merge被取消"。 | `part_log`中`error IN (236, 241)`的先后序列 | P-11 |
| 394 | QUERY_WAS_CANCELLED | 客户端断开或`KILL QUERY`。通常是*客户端*先于服务器超时。 | 与`query_duration_ms`分布配对看 | |
| 439 | CANNOT_SCHEDULE_TASK | 线程池耗尽（`max_thread_pool_size`、后台线程池）。这是饱和的症状，不是原因。 | `metric_log`的线程池列；`host_info.cpu.load_avg` | P-22 |
| 460 | CANNOT_CREATE_TIMER | 因为没有可用线程而创建定时器失败——与439同类；只是一个时间锚点，不是根因。 | 同上 | P-22 |
| 128 | TOO_LARGE_ARRAY_SIZE | 数组/字符串大小限制（`max_array_size_as_field`、`format_*`）——通常是数据问题。 | `query_log_details.exception` | |
| 306 | TOO_DEEP_RECURSION | 查询/表达式嵌套过深。 | | |
| 1000 | POCO_EXCEPTION | 包装的Poco错误：`No thread available`、socket错误、`Timeout`。要读消息内容。 | `text_log`、`logs/` | P-22 |
| 1001 | STD_EXCEPTION | 包装的`std::exception`（`std::out_of_range`、`bad_alloc`……）。在merge中，它常指向该版本上的数据/序列化bug。 | `part_log`中`error = 1001`及`exception`；版本 | P-30 |
| 1002 | UNKNOWN_EXCEPTION | 其他情况；常见于*客户端*一侧（`Broken pipe`、连接重置）。 | `query_log_details.exception` | |

## parts（数据分片）、merges（合并）、mutations（变更）、存储

| 错误码 | 名称 | 通常意味着 | 查看 | 模式 |
|---|---|---|---|---|
| 243 | NOT_ENOUGH_SPACE | 磁盘已满或低于`min_free_disk_*`；merge需要约等于结果part大小的可用空间。 | `system.disks.free_pct`；`system.parts`中最大的part | HC-Disk |
| 40 | CHECKSUM_DOESNT_MATCH | part数据损坏（位翻转、坏盘、写入中断）。该part通常会被分离为`broken`（detached part，分离part）。 | `system.detached_parts.reason`；`logs/` | P-31 |
| 226 | NO_FILE_IN_DATA_PART | part内缺少列/mark文件——损坏或ALTER被中断。 | `detached_parts`、`part_log` | P-31 |
| 233 / 232 | BAD_DATA_PART_NAME / NO_SUCH_DATA_PART | Keeper/元数据中记录的part不在磁盘上（或反之）——复制追赶或手动detach。 | `replication_queue.last_exception`、`detached_parts` | P-31 |
| 235 | DUPLICATE_DATA_PART | 同一个block从不同replica（副本）重复插入 / 某个replica重新挂载了已存在的part。 | `replication_queue` | |
| 246 | CORRUPTED_DATA | 通用损坏（压缩块、索引）。 | `detached_parts`、`logs/` | P-31 |
| 384 | PART_IS_TEMPORARILY_LOCKED | part被正在运行的merge/mutation固定住；针对它的DETACH/DROP PART/mutation会等待。 | `system.merges`、`system.mutations.parts_to_do_names` | P-14 |
| 341 | UNFINISHED | `ALTER ... SETTINGS mutations_sync` / `replication_alter_partitions_sync`等待超时；mutation仍在后台运行。 | `system.mutations` | P-14 |
| 517 | CANNOT_ASSIGN_ALTER | 同一张表上先前的mutation/ALTER未完成时，新的ALTER无法排队。 | `system.mutations`的年龄与`parts_to_do` | P-14, P-15 |
| 692 | TOO_MANY_MUTATIONS | 单表上未完成的mutation超过`number_of_mutations_to_throw`（默认1000）——通常是一条反复失败的mutation被不断重试。 | 按表统计`system.mutations`数量、`command` | P-15 |
| 359 | TABLE_SIZE_EXCEEDS_MAX_DROP_SIZE_LIMIT | 表大小超过`max_table_size_to_drop`（默认50 GB）时`DROP`/`TRUNCATE`被拒绝。 | `configuration/`；不是故障 | |
| 36 | BAD_ARGUMENTS | 无效的参数/设置组合。在启动时的服务器日志中：某个配置值无效（例如mutation线程池设置与`background_pool_size`不一致）。 | `logs/clickhouse-server.err.log`启动部分、`text_log` | P-16 |
| 53 / 70 | TYPE_MISMATCH / CANNOT_CONVERT_TYPE | MV/插入与其目标之间的schema漂移，或失败的`MODIFY COLUMN` mutation（NULL → 非Nullable）。 | `system.mutations.command`；按`tables`查`query_log_details` | P-15, P-32 |
| 349 | CANNOT_INSERT_NULL_IN_ORDINARY_COLUMN | 插入/MV向非Nullable列写入NULL。 | `query_log_details.exception` | P-32 |
| 117 | INCORRECT_DATA | 输入数据与声明类型不符（JSON路径类型提示、畸形值）——如果坏值已经落盘，插入*和* merge都会失败。 | `part_log`中`error = 117`；`query_log_details`的插入 | P-32 |
| 6 / 27 / 72 / 38 | CANNOT_PARSE_TEXT / CANNOT_PARSE_INPUT_ASSERTION_FAILED / CANNOT_PARSE_NUMBER / CANNOT_PARSE_DATE | 客户端发送了畸形行（CSV/JSON/TSV）。客户端侧的数据问题。 | `query_log_details`的`exception_code`、`interface`、`user` | |
| 10 | NOT_FOUND_COLUMN_IN_BLOCK | block中缺少期望的列——MV/目标schema不匹配，或该版本上的bug。 | 涉及的`tables`；版本 | P-32 |

## 复制与Keeper

| 错误码 | 名称 | 通常意味着 | 查看 | 模式 |
|---|---|---|---|---|
| 999 | KEEPER_EXCEPTION | 任何ZooKeeper/Keeper错误：会话过期、连接丢失、操作超时、节点已存在 / 节点不存在。消息中带有Keeper的错误码（`Session expired`、`Connection loss`、`Operation timeout`）。突发 = Keeper不可用、饱和或网络分区。 | 按小时看`metric_log.zk_hw_exceptions`；`system.replicas.is_session_expired`；`replication_queue.last_exception` | P-40, P-41 |
| 319 | UNKNOWN_STATUS_OF_INSERT | "We tried to commit part, but ZooKeeper is unavailable, insert status is unknown, client must retry"：part可能已提交，也可能没有。只在Keeper不可用时出现。 | 在出现999的同一时段内`query_log_details`的`Insert`行；`metric_log_coordination`的硬件异常；`part_log`的NewPart与后续重复 | P-40, P-57 |
| 571 | DATABASE_REPLICATION_FAILED | "ZooKeeper session expired or replication stopped, try again"：某个Replicated *数据库*无法处理其DDL日志。在Keeper失联期间于`Replicated`数据库执行`CREATE`/`DROP`时出现；之后DDL会被重放。 | `distributed_ddl_queue`（status、exception_code）、`system.databases`的引擎计数、`text_log_histogram`中的`DDLWorker`/`DatabaseReplicated` | P-57 |
| 221 | NO_SUCH_INTERSERVER_IO_ENDPOINT | 某个replica向本服务器请求了一个未注册的interserver端点（part fetch，或共享存储集群上的`SharedMergeTreePartsUpdate:/…/virtual_parts/<replica>`）——表仍在加载、刚被（重新）创建，或服务器正在重启。重启后立刻出现数万条的突发是正常的恢复噪声；持续出现 = 某张表始终没有恢复。 | `system.errors`的value对比`Uptime`；`logs/*.err.log`的`InterserverIOHTTPHandler`；`text_log_histogram` | P-57 |
| 86 | RECEIVED_ERROR_FROM_REMOTE_IO_SERVER | 221的*另一侧*：本服务器调用对端的interserver端点并收到HTTP错误（500，响应体中是对方的221）。原因相同，只是从调用方看到。 | `system.errors.last_error_message`会写明对端URL和端点 | P-57 |
| 225 | NO_ZOOKEEPER | 服务器启动时没有Keeper配置，或某张复制表缺少Keeper配置。 | `configuration/`的`<zookeeper>`/`<keeper_server>` | P-42 |
| 242 | TABLE_IS_READ_ONLY | replica丢失了Keeper会话 / 元数据不匹配 / 磁盘已满；在恢复前拒绝插入。 | `system.replicas.is_readonly`、`is_session_expired`；`system.disks` | P-40 |
| 164 | READONLY | 用户/profile为`readonly=1`，或*服务器*处于只读模式（磁盘耗尽）。与242不同。 | 按`user`查`query_log_details`；`configuration/users.d` | |
| 244 / 308 | UNEXPECTED_ZOOKEEPER_ERROR / UNEXPECTED_NODE_IN_ZOOKEEPER | Keeper元数据与本地状态不一致（残留znode、恢复到已存在的路径）。 | `replication_queue.last_exception` | P-43 |
| 253 | REPLICA_ALREADY_EXISTS | 在已有该replica名称的`Replicated*`路径上执行`CREATE TABLE`——DROP/重建后Keeper中的残留元数据。 | `logs/`中的DDL错误 | P-43 |
| 224 | REPLICA_IS_ALREADY_ACTIVE | 两台服务器声明了同一个replica名称（配置`<macros>`重复）。 | `configuration/`的macros | P-42 |
| 285 | TOO_FEW_LIVE_REPLICAS | quorum插入（`insert_quorum`）无法达到足够的active replica。 | `system.replicas_problem_tables`的`active_replicas/total_replicas` | |
| 286 / 319 | UNSATISFIED_QUORUM_FOR_PREVIOUS_WRITE / UNKNOWN_STATUS_OF_INSERT | quorum插入的边界情况；319 = 客户端无法得知插入是否落盘（只有在启用去重时才能安全重试）。 | | |
| 389 | INSERT_WAS_DEDUPLICATED | block哈希已见过——插入被静默跳过（客户端重试后属正常；当*每次*插入都被去重时才是问题）。 | `text_log`的 "Deduplication path already exists"；`query_log_details`的`written_rows = 0` | P-33 |
| 741 | TABLE_UUID_MISMATCH | 分析时解析出的表UUID在执行前发生了变化（EXCHANGE/可刷新MV交换的竞态）。与Keeper无关。 | `text_log`；`system.tables`中的可刷新MV | |
| 529 | NOT_A_LEADER | 在要求leader的拓扑中，操作被发到了非leader replica（旧版本）。 | | |

## 查询、schema、访问

| 错误码 | 名称 | 通常意味着 | 查看 | 模式 |
|---|---|---|---|---|
| 47 | UNKNOWN_IDENTIFIER | 列/别名不存在——客户端bug或schema变更。 | `query_log_details`的`query`样本、`type = 'ExceptionBeforeStart'` | |
| 60 / 81 | UNKNOWN_TABLE / UNKNOWN_DATABASE | 对象不存在——已删除/重命名、数据库错误，或EXCHANGE期间的竞态。注意：这些行在`query_log_details`中`tables = ''`。 | 同上 | |
| 57 | TABLE_ALREADY_EXISTS | 没有`IF NOT EXISTS`的重复`CREATE TABLE`；除非反复循环，否则无害。变体：在`CREATE OR REPLACE TABLE db.`.tmp.inner_id.<uuid>``上、带`/* ddl_entry=query-N */`注释时出现 "Mapping for table with UUID=… already exists. It happened due to UUID collision" = 某个Replicated数据库在Keeper会话丢失后**重放** materialized view（物化视图）DDL，并与自己的第一次尝试发生冲突。 | `distributed_ddl_queue`（同一query出现在多个条目下）、`query_log_details`中`user`为空的`Create`行 | P-57 |
| 84 | DIRECTORY_ALREADY_EXISTS | merge/fetch试图创建已存在的part目录——半提交操作之后的重试（Keeper会话丢失后很典型）。停止后无害；但要统计次数。 | 事故时段内`part_log`中带`error = 84`的`MergeParts`/`DownloadPart` | P-57 |
| 504 | FILE_ALREADY_EXISTS | 对象存储中与84同族（"Object data/<uuid>/<part>/ already exists: While executing operation #N"）：重试的提交发现blob前缀已写入。 | `part_log`的`error = 504`、`blob_storage_log`的Upload行 | P-57 |
| 107 | FILE_DOESNT_EXIST | 元数据声称存在的part文件（`primary.idx`、`<col>.cmrk2`、`data.bin`）无法读取——"attempt to read data part X (state Active) failed … It can mean that some retryable error happened, or data is corrupted"。在对象存储磁盘上，它意味着元数据（本地`.txt`或 **Keeper持有**）指向的blob已不存在或从未写入；同一消息中的`The specified key does not exist`（S3 `NoSuchKey`）是同一问题在下一层的表现。 | 按表统计`query_log_details`中的107；该小时的`blob_storage_log` Delete/Upload；每个replica的`system.metrics` `MetadataFromKeeperCacheObjects`；更早的`part_log` 84/504 | P-58 |
| 735 | QUERY_WAS_CANCELLED_BY_CLIENT | 客户端发送了Cancel包（客户端侧超时、用户中断、连接池回收）。本身不是服务器故障；事故期间出现突发意味着客户端放弃等待卡住的插入/DDL。 | 按`user`和小时查`query_log_details` | P-21 |
| 62 / 80 | SYNTAX_ERROR / INCORRECT_QUERY | 客户端发送了无效语句。 | 按`user`、`interface` | |
| 43 / 44 / 46 | ILLEGAL_TYPE_OF_ARGUMENT / ILLEGAL_COLUMN / UNKNOWN_FUNCTION | 查询bug或版本不匹配（该版本没有该函数）。 | 版本、`query`样本 | |
| 48 | NOT_IMPLEMENTED | 不支持该功能（引擎、格式、在此版本/此部署上）。 | 版本；`system.tables`中的引擎 | |
| 115 / 452 / 472 | UNKNOWN_SETTING / SETTING_CONSTRAINT_VIOLATION / READONLY_SETTING | 客户端设置了该版本不存在的设置，或违反了profile约束。 | `configuration/users.d`的constraints | |
| 184 | ILLEGAL_AGGREGATION | 嵌套聚合函数 / 在WHERE中使用聚合。 | | |
| 277 | INDEX_NOT_USED | 设置了`force_index_by_date` / `force_primary_key`，而查询未使用索引——通常是刻意的护栏。 | `configuration/users.d` | |
| 344 | SUPPORT_IS_DISABLED | 该功能被设置禁用（experimental开关、`allow_*`）。 | | |
| 497 | ACCESS_DENIED | 缺少grant（授权）。**collector（采集器）本身需要`SELECT ON system.*`、`SHOW …`，cloud模式下还需要`REMOTE` + `CREATE TEMPORARY TABLE`**——如果这些出现在工具自己的输出中，说明bundle不完整。注意：2026年9月之前的工具版本在`too_many_parts` alert文本中写的是 "code 497"——这是笔误；TOO_MANY_PARTS是252。 | 按`user`查`query_log_details`；`configuration/users.d` | |
| 516 / 193 / 192 / 195 | AUTHENTICATION_FAILED / WRONG_PASSWORD / UNKNOWN_USER / IP_ADDRESS_NOT_ALLOWED | 客户端凭据 / 网络ACL问题；突增通常意味着客户端配置错误或扫描器。 | `query_log_details`的`user`、`interface`；`logs/` | |
| 511 | UNKNOWN_ROLE | grant/profile中引用的角色不存在。 | `configuration/users.d` | |
| 507 | UNABLE_TO_SKIP_UNUSED_SHARDS | 设置了`force_optimize_skip_unused_shards`，而查询无法裁剪shard（分片）。 | | |
| 519 | NO_REMOTE_SHARD_AVAILABLE | 分布式查询找不到某个shard的存活replica。 | `system.clusters.errors_count` | P-44 |
| 279 | ALL_CONNECTION_TRIES_FAILED | 分布式/远程查询无法到达某个shard的任何replica。 | `system.clusters` | P-44 |
| 574 | DISTRIBUTED_TOO_MANY_PENDING_BYTES | 异步`Distributed`插入积压超过`bytes_to_throw_insert`。 | | P-44 |
| 156 / 36 | DICTIONARIES_WAS_NOT_LOADED (+ source errors) | 字典加载失败：源不可达、凭据错误、schema不匹配。 | `system.dictionaries.status/last_exception` | HC-Dict |

## 网络、文件、操作系统

| 错误码 | 名称 | 通常意味着 | 查看 | 模式 |
|---|---|---|---|---|
| 209 / 210 | SOCKET_TIMEOUT / NETWORK_ERROR | client↔server或server↔replica/S3之间的超时或重置。在`part_log`的fetch（`DownloadPart`）中 = interserver问题。 | `part_log`的`event_type = 'DownloadPart' AND error != 0`；`text_log` | P-44 |
| 198 | DNS_ERROR | 主机名解析失败（remote server、Keeper、S3端点）。 | `configuration/`的主机名；`logs/` | P-44 |
| 203 | NO_FREE_CONNECTION | 连接池耗尽（distributed、S3）。 | | |
| 95 / 96 / 33 / 32 | CANNOT_READ_FROM_SOCKET / CANNOT_WRITE_TO_SOCKET / CANNOT_READ_ALL_DATA / ATTEMPT_TO_READ_AFTER_EOF | 对端在流中途关闭连接（客户端断开、LB空闲超时、Keeper抖动）。 | `query_log_details`的`interface`；`text_log` | |
| 499 | S3_ERROR | 对象存储错误：限流（SlowDown/503）、AccessDenied、对象缺失、凭据。S3后端磁盘上的突发会拖住merge和读取。 | `text_log`/`logs/`的消息文本；`part_log`错误；`system.disks.type` | P-45 |
| 76 / 107 / 74 | CANNOT_OPEN_FILE / FILE_DOESNT_EXIST / CANNOT_READ_FROM_FILE_DESCRIPTOR | 文件系统问题：权限、文件被删除、`LimitNOFILE`过低（"Too many open files"）。 | `host_info.clickhouse_relevant_tunables.clickhouse_open_files_*`；`logs/` | P-13 |
| 204 | CANNOT_FSYNC | fsync时的磁盘I/O错误——存储故障。 | `logs/`、`dmesg`（需询问） | |
| 412 | NETLINK_ERROR | TaskStats/netlink不被允许（容器中）——无害，会禁用每线程的OS指标。 | `logs/`启动部分 | |
| 139 / 318 | NO_ELEMENTS_IN_CONFIG / INVALID_CONFIG_PARAMETER | 配置缺失或无效；启动时服务器可能拒绝加载某个section。 | `configuration/`；`logs/`启动部分 | P-16 |
| 49 | LOGICAL_ERROR | 内部不变量失败——是bug。同一代码路径频繁重复 + 该版本 = 先检查changelog和未关闭的issue，再查其他。 | `text_log`/`logs/`的消息与堆栈；版本 | P-30 |

## 阅读提示

- `system.errors.value`是自服务器启动以来的累计值。在90天运行时长里出现10 000次`MEMORY_LIMIT_EXCEEDED`，与3小时内出现10 000次完全不同——始终要结合`uptime`。
- `query_log_details.type`告诉你失败发生在*何时*：`ExceptionBeforeStart`（解析、分析、访问、202/497/62之类的限制）vs `ExceptionWhileProcessing`（运行时：241、159、252、999……）。
- `exception_code`旁的`distinct_exceptions`：400个事件 / 1条不同消息 = 一个反复出现的故障；400 / 400 = 许多不同消息共享同一个错误码（例如47对应不同列名）。
- 客户端侧的错误码（带 "Broken pipe" 的1002、394、客户端的210）通常是服务器侧原因*之后*的结果（慢查询撞上负载均衡器超时）。先在同一小时里找服务器侧的错误码。