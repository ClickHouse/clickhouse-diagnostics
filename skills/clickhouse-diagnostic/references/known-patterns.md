# 已知模式——ClickHouse常见问题在bundle（诊断包）中的表现

提炼自ClickHouse支持经验（公开文档、ClickHouse工程指导，以及约100个已关闭支持升级案例的匿名样本，2023–2026）。每条条目都从**你在bundle（诊断包）中能看到什么**的视角来写；这里不会识别任何客户、服务或表。仅云端的机制（pod、operator、Keeper自动扩缩）只在会改变OSS/on-prem读者结论的地方提及。

匹配模式时的用语规则：“证据**与**P-nn**一致**” / “这**看起来像**……”——绝不说“这是一个已知问题”或“你命中了bug #N”（见`clickhouse-source.md` §4）。模式匹配只是一个假设，需要结合*验证*下列出的第二个来源来确认。

## 快速参考（先扫描，再只打开匹配的条目）

| Id | 症状优先标签 | 涉及的bundle文件 |
|---|---|---|
| P-01 | part（数据分片）数量暴涨——INSERT速率超过merge（合并）能力 | parts, part_log, merges, metric_log |
| P-02 | 大量微小INSERT（每个part仅数百行） | parts, part_log NewPart, query_log Insert |
| P-03 | 分区键粒度过细 / 分区数量上千 | tables.partition_key, parts |
| P-04 | 积压增长，但merge池看起来空闲 | merges, part_log, metric_log, tables.engine_full |
| P-05 | 行级DELETE/UPDATE风暴造成mutation（变更）积压 | mutations, part_log MutatePart |
| P-10 | MEMORY_LIMIT_EXCEEDED——读懂消息类别 | query_log exception, errors |
| P-11 | merge/mutation循环触发OOM并卡住相关part | part_log 241/236, merges, mutations |
| P-12 | 内存基线逐日抬升（泄漏、流量、缓存之别） | metric_log, query_log, host_info |
| P-13 | 操作系统层面耗尽：173、"Too many open files"、THP、cgroup | errors, host_info, logs |
| P-14 | mutation "卡住"：killed ≠ finished，队头被merge阻塞 | mutations, merges, part_log |
| P-15 | TOO_MANY_MUTATIONS / 单个失败mutation无限重试 | mutations, part_log 53/70/117/349 |
| P-16 | 启动时设置组合无效（Code 36） | logs startup, configuration |
| P-17 | 系统日志表吞噬磁盘、merge内存和CPU | parts (system db), part_log, configuration |
| P-18 | TTL未生效 / 过期数据未被删除 | parts delete_ttl_info, part_log TTL merges, tables |
| P-20 | TOO_MANY_SIMULTANEOUS_QUERIES——是请求堆积，不是流量增长 | query_log 202, processes, metric_log |
| P-21 | 超时：服务端限制、客户端取消、分布式DDL | query_log 159/394, text_log |
| P-22 | 线程耗尽症状（439/460/1000 "No thread available"） | errors, text_log, metric_log |
| P-30 | 某一版本上反复崩溃或LOGICAL_ERROR | crash_log, logs Fatal, version |
| P-31 | part损坏/不可读：数据损坏或版本错配 | detached_parts, errors 40/226/33, logs |
| P-32 | MV（物化视图，materialized view）/源表/目标表之间的schema漂移，类型转换失败 | tables DDL, query_log 53/70/117/349, part_log |
| P-33 | INSERT "成功"但行缺失、重复或错误 | query_log Insert, text_log dedup, async_insert_log, tables (MV) |
| P-34 | MV链放大每次INSERT（并可能阻塞异步flush） | tables MV count, part_log NewPart per target, async_insert_log |
| P-35 | MV运行正常却不写入任何数据 | query_views_log, tables.as_select, text_log |
| P-36 | 基础part提交后MV推送抛异常 | query_views_log, query_log Insert, text_log |
| P-40 | Keeper会话丢失 → replica（副本）只读，999错误爆发 | replicas, errors 999/242, metric_log zk_*, text_log |
| P-41 | replication queue（复制队列）卡住 / absolute_delay过大 | replication_queue, replicas, configuration |
| P-42 | 拓扑/配置错误：interserver host、macros、quorum、新节点 | configuration, clusters, replicas |
| P-43 | Keeper元数据陈旧：幽灵replica、孤立UUID、旧block编号 | replicas total vs active, errors 253/244/308, logs |
| P-44 | 网络/DNS/interserver故障 | clusters.errors_count, errors 198/209/210/279, part_log DownloadPart |
| P-45 | 对象存储（S3）限流、延迟、迁移与磁盘tier（分层） | errors 499, text_log, disks, part_log |
| P-50 | 会使裁剪失效的ORDER BY / 分区设计 | tables sorting/partition_key, query_analysis text_log_parts |
| P-51 | 在Replacing/Collapsing表上使用FINAL——全表扫描 | query_log query sample, tables engine |
| P-52 | GROUP BY / JOIN内存与join构建侧 | query_log 241 message, query sample |
| P-53 | 索引未生效：skip index/LIKE不匹配、analyzer行为变化 | tables indices in DDL, query sample, version |
| P-54 | 升级后性能回退（性能、内存、CPU、行为） | version, query_log by hour, part_log, logs |
| P-55 | 监控/内部工具主导查询负载 | query_log by user, tables count |
| P-56 | 分布式`INSERT … SELECT` / `s3Cluster`加载慢：worker shard（分片）、文件倾斜、发起端插入 | query_analysis (query_details, profile_events, executions_timeline, text_log_full), part_log |
| P-57 | Keeper quorum丢失 / leader反复切换 → 会话过期级联：INSERT出现999与319，merge停止（随后252），Replicated数据库DDL重放（571、57 UUID冲突），恢复期间interserver 221/86风暴 | metric_log_coordination, zookeeper_connection, zookeeper_log, part_log errors, query_log by hour, distributed_ddl_queue, databases |
| P-58 | Keeper或对象存储事故后，读取`Active`的part时因107 FILE_DOESNT_EXIST / `The specified key does not exist`失败——part元数据陈旧（本地或Keeper持有的缓存） | query_log 107 by table, system.metrics `MetadataFromKeeperCacheObjects` per replica, blob_storage_log, disks/storage_policies, part_log 84/504 |
| P-59 | SharedMergeTree replica对同一查询返回不同行数；新part在部分replica上延迟数分钟到数小时才出现——后台fetch/schedule池饱和（修复：调整池大小，通过配置重载即时生效） | metric_log_7_days pool columns vs server_settings sizes; metric_log_by_host_3_days (per host: pool vs live size, fetch attempts vs parts selected, elections, Keeper ms/tx); cloud replicas (`hostname`, `inserts_in_queue`, `oldest_part_to_get`); merge_tree_settings; tables.total_rows; alerts `fetch_pool_saturated` / `schedule_pool_saturated`; execution_log header (`node:`, `uptime:`) |
| P-61 | 某个成员落后后Keeper集群失去leader——它需要的快照超过2 GiB，且以单块方式发送 | keeper/<member>.txt (srvr, mntr, lgif, conf), zookeeper_connection, P-57 files |
| P-62 | INSERT去重路径上的remove淹没Keeper，多数返回ZNONODE——各replica在裁剪相同记录 | aggregated_zookeeper_log_paths_1_day (async_blocks, deduplication_hashes), _by_hour_2_days (Remove errors), part_log, asynchronous_insert_log, settings |
| P-63 | Znode堆积（磁盘元数据`_objects`的create远多于remove），直到Keeper内存耗尽 | aggregated_zookeeper_log_paths_1_day (net_creates), keeper/<member>.txt zk_znode_count, part_log, parts, settings compatibility |
| P-64 | 对象存储元数据读风暴——`_objects`上的MultiRead内出现数百万次Get（每part文件数、JSON设置、重启、性能回退） | aggregated_zookeeper_log_paths_1_day (in_multi), _by_minute_1_day bursts, parts/columns, settings compatibility, text_log_keeper |
| P-65 | 某个replica的Keeper延迟达到数秒，而其他replica约为10 ms | aggregated_zookeeper_log_by_minute_1_day per hostname, metric_log_by_host_3_days, zookeeper_connection, keeper/<member>.txt |
| P-66 | 早期25.10的Keeper客户端缺陷：Multi缓慢、CreateIfNotExists挂起、重连耗时数分钟 | aggregated_zookeeper_log_by_hour_2_days latency, text_log_keeper_1_day, version, server_settings |
| P-67 | 分区操作同步大量小part时Remove/Check/Create缓慢 | aggregated_zookeeper_log_by_hour_2_days latency, part_log, query_log_details, columns |
| P-68 | 某个后台任务在Keeper上循环（重试fetch或merge步骤） | aggregated_zookeeper_log_by_hour_2_days (component), _paths_1_day, replication_queue, replicas |

---

## Part（数据分片）与merge（合并）

### P-01 part数量暴涨——INSERT速率超过merge能力
**你看到：**`system.parts`（active）在一个分区中有数百到数千个part，许多处于`level = 0`；`part_log`显示每小时`NewPart`事件远多于`MergeParts`；`metric_log.max_merge_pool_tasks`钉在池大小；`query_log_details`中INSERT出现252 `TOO_MANY_PARTS`；同一时段有时还有CPU限流。
**通常意味着：**一个反馈环——part越多，merge选择越慢，于是调度的merge越少，part继续增长。它不会自我纠正。触发因素：INSERT速率阶跃上升、行级删除/更新突发（P-05）、另一张表导致merge池饱和、后台短暂停顿（Keeper、S3）让merge停了几分钟。
**验证：**按小时统计该表的`NewPart`数对比`MergeParts`数；level-0 part的`avg(rows)`（很小 = P-02）；`system.merges`中长时间运行/卡住的merge（P-04、P-11）。
**修复/缓解：**停止或限流INSERT，让merge追上来；写更大的批次（1万–10万行）或启用`async_insert`；临时提高`min_parts_to_merge_at_once`（≈50）然后**重置**它；只在短期保险时提高`parts_to_throw_insert`/`parts_to_delay_insert`，之后恢复默认值（≥ 23.6的OSS默认值为3000/1000；更早版本为300/150）；只有CPU空闲时才提高`background_pool_size`。爆发期间`OPTIMIZE TABLE`很少有用——它会和后台merge竞争。
**不要混淆：**P-03（分区很多，但每个分区part很少）和P-04（池空闲但选择器没有调度）。
**公开参考：**文档 → MergeTree设置（`parts_to_throw_insert`、`parts_to_delay_insert`、`max_parts_in_total`），“Asynchronous inserts”。

### P-02大量微小INSERT
**你看到：**`part_log`的`NewPart`中`size_in_bytes/count`只有KB级，单表每小时有数千个事件；`query_log_details`的`Insert`每小时数千次，`written_rows/count` < 1000；活跃part的`rows`只有数百。
**通常意味着：**客户端每个事件/行发送一条小INSERT（或连接器批次大小很小）。每次INSERT = 每个分区一个part，每个MV目标再各一个part。
**验证：**按`user`/`interface`查看`query_log_details`中的插入模式；查看`system.tables`中该表上的MV（每个MV都会放大part创建）。
**修复：**在客户端批量（约10万行或1–5秒），或服务端启用`async_insert = 1`并设置`wait_for_async_insert = 1`（除非可以接受静默丢失，否则保持为1）。连接器：提高批次大小/超时，而不是降低。对于`INSERT … SELECT`迁移，使用较大的`min_insert_block_size_rows/bytes`。
**公开参考：**文档 → “Asynchronous inserts”、“Bulk inserts”最佳实践。

### P-03分区键过细 / 分区过多
**你看到：**`system.tables.partition_key`形如`toStartOfHour(ts)`、`(toDate(ts), tenant)`、`xxh3(...) % N`；`system.parts`中每张表有数千个`partition_id`，每个只有少量小part；TOO_MANY_PARTS消息提到 "in all partitions in total"（`max_parts_in_total`，默认100000）或 "This indicates wrong choice of partition key"。
**通常意味着：**分区被当作索引使用。merge从不跨分区，因此旧的小分区永远不会合并，而一次触及N个分区的INSERT会创建N个part。多级保留键（`(date, retention_bucket)`）会在长保留分区中累积许多小part。
**验证：**每个分区的part分布；旧分区的`avg_parts_per_partition`；插入形态（一次插入是否跨越多个分区？）。
**修复：**最多按月/天分区（一个分区应容纳GB级数据），不要把tenant/product放进键中；对已有的长保留分区定期运行`OPTIMIZE TABLE … PARTITION ID '…' FINAL`；重度分区的插入先经由`Null`表 + MV暂存。提高`max_parts_in_total`只会推迟问题。
**公开参考：**文档 → “Custom partitioning key”（“Partitioning is not intended to speed up SELECT queries”）。

### P-04积压增长，但merge池看起来空闲
**你看到：**part数量很高（P-01），但`system.merges`只有0–2个merge，`metric_log.avg_merge_pool_tasks`远低于池大小，CPU空闲；`part_log`每小时只有少量`MergeParts`；有时`text_log`重复出现 "Not executing … because N merges already executing"、"Another merge assigned for some of our source parts"，或某个merge选择任务在退避。
**通常意味着：**以下之一：(a) 一个巨大或无限重试的merge占住槽位（S3错误、OOM循环 → P-11），并阻塞该分区的merge判定条件；(b) `min_age_to_force_merge_seconds`（或`_on_partition_only`）强制了过大的整分区merge，无法完成；(c) part达到约3万以上时，每次选择扫描（数百毫秒）只产出一个merge——瓶颈是选择而不是执行；(d) 中间层级（L2–L4）卡在约150 GB的结果大小上限；(e) 在Cloud/SharedMergeTree上，merge协调器的prepare数量上限，或各replica的`background_pool_size`不一致。
**验证：**`system.merges`中`elapsed`以小时计且`progress`不动的行；`engine_full`中的`min_age_to_force_merge_seconds`、`max_parts_to_merge_at_once`、`max_bytes_to_merge_at_max_space_in_pool`；同一分区上的`part_log`错误。
**修复：**解除占用者（对OOM merge执行kill/扩容，修复S3，临时设置`min_age_to_force_merge_seconds = 0`）；如果巨大merge会OOM，降低`max_parts_to_merge_at_once`以分散工作；批量插入以减少选择集合；恢复期间可考虑提高`max_parts_to_merge_at_once`并用`parts_to_throw_insert`兜底，之后改回。
**公开参考：**文档 → MergeTree设置（`max_bytes_to_merge_at_max_space_in_pool`、`min_age_to_force_merge_seconds`、`max_parts_to_merge_at_once`）。

### P-05行级DELETE/UPDATE风暴
**你看到：**`system.mutations`中同一张表有数百个`UPDATE _row_exists = 0 WHERE …`/`DELETE WHERE …`命令；`part_log`中`MutatePart`事件占主导；part数量上升（P-01），`mergeSelectingTask`/mutation CPU很高；mutation出现`MEMORY_LIMIT_EXCEEDED`。
**通常意味着：**应用发出大量小的轻量删除/更新；每次mutation都必须重写（或屏蔽）每个part，且mutation按part串行。每个待处理mutation还会在内存中保留解析后的命令。
**验证：**按表和`create_time`小时统计mutation数量；`parts_to_do`对比总part数；`engine_full`中的`max_number_of_mutations_for_replica`/`number_of_mutations_to_throw`。
**修复：**把删除合并成更少的语句；对按时间或tenant限定的删除优先使用`DROP PARTITION`/`REPLACE PARTITION`；用软删除建模（ReplacingMergeTree `is_deleted`、`argMax`）而不是mutation；用`max_number_of_mutations_for_replica`限制并发；`lightweight_deletes_sync = 0`以避免客户端超时（删除仍会在后台生效）。
**公开参考：**文档 → “Lightweight DELETE”、“ALTER TABLE … DELETE”、“Manipulating partitions”。

## 内存、mutation、TTL、配置

### P-10 MEMORY_LIMIT_EXCEEDED——先读懂消息类别
**你看到：**`query_log_details`/`system.errors`中`exception_code = 241`；`exception`文本会告诉你哪个限制、发生在哪里：
- `Memory limit (for query) exceeded` → `max_memory_usage`（每查询）。
- `(total) memory limit exceeded … current RSS: X, maximum: Y` → 服务端限制（`max_server_memory_usage` / `_to_ram_ratio`，默认RAM或cgroup的0.9）。如果RSS ≈ maximum且伴随 "attempt to allocate chunk of 0.00 B"，说明整个服务器已到上限；失败的查询是受害者，不是原因——要找出谁占着内存（merge、其他查询、缓存，P-12）。
- `Memory limit (for user) exceeded` → `max_memory_usage_for_user`；同一用户的并发查询共享它。"would use 350 GiB … maximum 186 GiB" 也可能是async-insert/MV扇出下tracker记账导致的（P-34）。
- `While executing AggregatingTransform` → GROUP BY状态（P-52）。`FillingRightJoinSide` → JOIN构建侧（P-52）。`while pushing to view` → MV链（P-34）。`part_log`中的`While executing MergeTreeSequentialSource` / `(while reading column X)` → 一次merge（P-11）。`ArrowBlockInputFormat`/`Parquet…` → 大批次输入解析。
- `OvercommitTracker decision: Query was selected to stop` → 总量耗尽时overcommit tracker选中了该查询；`memory_overcommit_ratio_denominator = 0`可保护查询不被选中，但不保证成功。
**验证：**故障所在小时对比`metric_log.avg_memory_tracking_bytes`；同一小时的`part_log.peak_memory_usage`；并发查询数。
**修复：**取决于类别——见P-11、P-12、P-52；只有在识别出消耗者后才提高限制。在Cloud上，“service too small for the inserts”是真实答案（提高最小值）。
**公开参考：**文档 → “Memory limit exceeded” KB、`max_memory_usage`、`max_server_memory_usage_to_ram_ratio`、“Memory overcommit”。

### P-11 merge或mutation循环OOM并卡住相关part
**你看到：**某张表/某个分区的`part_log`连续数天交替出现`MergeParts error 241 (… while reading column X)`和`error 236 Cancelled merging parts (ABORTED)`；`system.merges`显示同一结果part的`progress`很低、`elapsed`很高；`system.mutations`的`parts_to_do_names`指向该分区的part，`parts_to_do`不动；该分区part数量在增长。
**通常意味着：**merge需要的内存超过服务器能提供的量（某列值巨大、JSON/Object列有大量动态路径、宽聚合状态、某分区含19+ GB的part）。merge判定会排除已分配给正在运行/重试merge的part，因此其后的mutation永远等待，看起来像“卡住”。
**验证：**失败merge的`part_log.peak_memory_usage`对比限制；消息中点名的列；`system.tables` DDL中的`JSON`/`Object`/`Map`列；`merges.memory_usage`。
**修复：**临时扩容内存（或释放内存——清空mark cache、暂停其他工作）让merge完成一次，之后mutation会自行排空；降低`max_parts_to_merge_at_once`；受影响版本的JSON列可考虑`object_shared_data_serialization_version = 'map_with_buckets'`（仅元数据，对后续merge生效）或减少`max_dynamic_paths`；对MV/projection密集的插入，降低`min_insert_block_size_bytes`。最后手段是detach该分区（RMT：`DETACH PARTITION`会取消已分配的merge），再重新attach健康的part。
**不要混淆：**P-14（健康的队头阻塞，0错误）和P-30（merge以1001/49失败——是缺陷，不是内存问题）。
**公开参考：**文档 → `max_parts_to_merge_at_once`、JSON数据类型（`max_dynamic_paths`、共享数据序列化）。

### P-12内存基线逐日抬升
**你看到：**`metric_log.avg_memory_tracking_bytes`（或主机RSS）逐日阶跃上升，直到重启；小查询出现241；缓存和已跟踪组件远低于RSS。
**通常意味着（按频率排序）：**(1) **流量增长**——更多并发查询/插入推高峰值，分配器将其保留为水位线：`query_log_details`中每查询`memory_usage/count`持平，而每小时`count`在增长；(2) **保留的元数据**——part非常多（主键 + marks在内存中：对`system.parts`的`primary_key_bytes_in_memory_allocated`求和）、保留数千个mutation（`finished_mutations_to_keep`）、巨大的字典（`system.dictionaries.bytes_allocated`）、大型mark/uncompressed/filesystem-cache元数据；(3) **特定版本的回退**——只有升级后基线才上升（P-54）；(4) 真正的泄漏——只有在排除 (1)–(3) 后才考虑，且需要分配器profile，而不是bundle。
**验证：**每小时的`count`和`memory_usage/count`趋势；`sum(primary_key_bytes_in_memory_allocated)`与part数；`configuration/`中的`mark_cache_size`/`uncompressed_cache_size`；`logs/`中的版本变更日期。
**修复：**若是 (1)，定位流量来源（user/table/hash）；若是 (2)，减少part数/字典/缓存；若是 (3)，升级或回滚。没有测量数据时不要建议调整分配器。
**公开参考：**文档 → “Memory overcommit”、服务器设置`mark_cache_size`、`uncompressed_cache_size`、`max_server_memory_usage_to_ram_ratio`。

### P-13操作系统层面耗尽
**你看到：**`system.errors`中173 `CANNOT_ALLOCATE_MEMORY`、76/107伴随 "Too many open files"；`host_info.clickhouse_relevant_tunables`：`transparent_hugepages = always`、`vm_overcommit_memory = 2`、`clickhouse_open_files_soft`很小、`vm_max_map_count` 65530；`cgroup_memory_limit_bytes`远低于`memory.total_bytes`；`logs/`启动警告提到同样问题。
**通常意味着：**是主机而不是ClickHouse的tracker拒绝了资源。在容器中，OOM killer统计的是工作集（RSS + page cache），因此`metric_log`中内存看起来正常时服务也可能被杀。
**验证：**`logs/clickhouse-server.log`中的启动横幅警告；重启但没有`crash_log`行（OOM kill不会留下记录）；`host_info.memory.available_bytes`。
**修复：**THP设为`madvise`/`never`；`vm.overcommit_memory = 0`；`LimitNOFILE` ≥ 500000；`vm.max_map_count` ≥ 262144；按cgroup限制调整`max_server_memory_usage_to_ram_ratio`；保持swap关闭。
**公开参考：**文档 → “Usage recommendations”（THP、overcommit）、“Requirements”。

### P-14 mutation “卡住”：killed ≠ finished，队头被merge阻塞
**你看到：**`system.mutations`中有些行已存在数小时/数天，`parts_to_do > 0`、`latest_fail_reason`为空（云bundle：`is_killed = 1`但仍被列出）；`ALTER`/`DROP INDEX`/`DROP PROJECTION`失败并报517 `CANNOT_ASSIGN_ALTER`或 "affected by mutation"；`part_log`在`parts_to_do_names`指名的part上显示长时间的`MergeParts`/`MutatePart`；part操作出现384 `PART_IS_TEMPORARILY_LOCKED`。
**通常意味着：**mutation按提交顺序逐part应用，并跳过当前分配给merge/mutation的part。一个巨大的part（数十GB、未分区表）或循环的merge（P-11）会阻塞链条。`KILL MUTATION`只设置killed标志——条目会一直保留，直到每个剩余part都被处理（走不重写捷径）——所以“已kill但未完成”在一段时间内是正常的。
**验证：**将`parts_to_do_names`与`system.merges.result_part_name`/源part以及`part_log`失败记录求交集；如果存在两个bundle，看`parts_to_do`趋势。
**修复：**等阻塞者完成后让它排空；不要继续kill更多mutation；对RMT，`DETACH PARTITION`会取消已分配的merge（再attach健康part）；当被kill的mutation卡在锁定part上时，`SYSTEM RESTART REPLICA db.table`可重置内存中的merge分配；长期方案是给表分区，让mutation分散到有界数量的part上，并避免在删除负载并发时执行重量级`MODIFY COLUMN`。
**公开参考：**文档 → “Mutations”、`KILL MUTATION`、`SYSTEM RESTART REPLICA`。

### P-15 TOO_MANY_MUTATIONS / 单个失败mutation无限重试
**你看到：**同一张表在`system.mutations`中有数百到1000行（在`number_of_mutations_to_throw`默认1000处出现692个`TOO_MANY_MUTATIONS`）；`part_log`的`MutatePart`带`error IN (53, 70, 117, 349)`，`exception`类似 "Cannot convert NULL value to non-Nullable type"、"Cannot parse … as Int64"、"Incorrect data"；新INSERT也可能以相同错误码失败。
**通常意味着：**现有数据无法满足的schema变更（`MODIFY COLUMN col String`但存在NULL；JSON路径类型提示与真实值矛盾；聚合状态类型变更）在第一个part上失败并被无限重试；应用还在后面不断添加mutation。
**验证：**最旧未完成mutation的`command`；失败part在`system.columns`中的数据类型。
**修复：**对失败的那个执行`KILL MUTATION`，修正类型（`Nullable(...)`、正确的提示）或源数据，然后重新下发；提高`number_of_mutations_to_throw`只会掩盖原因。
**公开参考：**文档 → `number_of_mutations_to_throw`、`ALTER TABLE MODIFY COLUMN`。

### P-16启动时设置组合无效
**你看到：**启动时`logs/clickhouse-server.err.log`：`Code: 36 … 'number_of_free_entries_in_pool_to_execute_mutation' (N) … is greater than … 'background_pool_size'*'background_merges_mutations_concurrency_ratio' … mutations cannot work with these settings`，或139/318配置错误；`configuration/`覆盖了耦合设置对的一侧；mutation从未被调度；DDL停滞。
**通常意味着：**MergeTree池预留设置必须低于`background_pool_size × background_merges_mutations_concurrency_ratio`（ratio默认2）。修改池大小（或平台自动扩缩它）会使固定覆盖失效。较高的`free_entries`值会*限流* mutation以保护merge——提高池大小才能让它重新合法。
**验证：**在`configuration/`中grep这两个设置；`system.tables.engine_full`中的逐表覆盖（取最高值）。
**修复：**只要覆盖了`number_of_free_entries_in_pool_to_execute_mutation`/`_optimize_entire_partition`，就显式固定`background_pool_size`；通过`metric_log.max_merge_pool_tasks`验证实际生效的池大小。
**公开参考：**文档 → 服务器设置`background_pool_size`、`background_merges_mutations_concurrency_ratio`；MergeTree `number_of_free_entries_in_pool_to_execute_mutation`。

### P-17系统日志表吞噬磁盘、merge内存和CPU
**你看到：**按`database`分组`system.parts`时，`system`位于最大之列；`part_log`中`system.metric_log`/`system.text_log`/`system.trace_log`上的merge具有最高`peak_memory_usage`（数十GiB），而用户表的merge只有MB级；`system.*`的merge出现241；`text_log`处于`trace`级别；`configuration/`的`<text_log>`/`<query_log>`段没有`<ttl>`；系统表插入突发时伴随突然的part爆炸。
**通常意味着：**日志表无界增长（默认无TTL），其宽compact part merge成本很高；`metric_log`尤其有数百列。
**验证：**`system.*_log`的`system.tables`引擎/TTL；`configuration/`日志段；`logs/`级别。
**修复：**给每个`<*_log>`段添加`<ttl>event_date + INTERVAL 30 DAY DELETE</ttl>`（或带TTL的`<engine>`）；生产环境把`text_log`级别设为`information`或更高；当merge OOM时可考虑在`metric_log`上设`min_bytes_for_wide_part = 0`；禁用你从不读取的日志（`trace_log`、`query_thread_log`，以及仅在有外部可观测性时才关闭高插入率下的`part_log`）。
**公开参考：**文档 → “System log tables”（`query_log`、`text_log`、`metric_log`配置：`ttl`、`partition_by`、`flush_interval_milliseconds`）。

### P-18 TTL未生效 / 过期数据未删除
**你看到：**`system.tables.create_table_query`有`TTL`，但旧分区的`system.parts`仍有超过TTL的行；最近写入的part上`delete_ttl_info_min/max` = `1970-01-01 …`；7天内`part_log`没有`merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')`或`RemovePart`事件；磁盘使用量上升（HC-4）。
**通常意味着：**(a) TTL merge被限流（`merge_with_ttl_timeout`默认每表4小时，`max_number_of_merges_with_ttl_in_pool`默认2），积压永远清不掉；(b) 某个`MODIFY TTL`被中断/杀死，新part写入时没有TTL信息——表需要`MATERIALIZE TTL`；(c) 使用了`materialize_ttl_after_modify = 0`且第3步（materialize）从未执行；(d) TTL表达式使用非确定性函数（`now()`），每次merge都重新求值；(e) `ttl_only_drop_parts = 0`会对永远排不上调度的大part强制重写行。
**验证：**最近创建的part与旧part的`delete_ttl_info_*`对比；`engine_full`中的TTL设置；`part_log`的TTL事件。
**修复：**表级`ttl_only_drop_parts = 1`、`merge_with_ttl_timeout` 600–1800、`max_number_of_merges_with_ttl_in_pool` 8–16（积压清掉后改回）；在低峰期执行`ALTER TABLE … MATERIALIZE TTL`（大表先设`materialize_ttl_recalculate_only = 1`）；使用基于列的TTL表达式。
**公开参考：**文档 → “TTL for columns and tables”、MergeTree设置`ttl_only_drop_parts`、`merge_with_ttl_timeout`。

## 并发与超时

### P-20 TOO_MANY_SIMULTANEOUS_QUERIES通常是请求堆积
**你看到：**`query_log_details`/`errors`中出现202（"Too many simultaneous queries. Maximum: 100/1000"）；每小时*已完成*查询的`count`并不比平时高，但`query_duration_ms/count`上升 ×10；`system.processes`（采集时）充满长时间`elapsed`的查询；同一时段常有159/241/999。
**通常意味着：**某些因素让查询变慢（Keeper问题P-40、merge/CPU/IO饱和、升级回退P-54、线程上限），于是到达速度超过完成速度，触发准入限制。两种特定的自伤变体：workload/scheduler的线程上限（`max_concurrent_threads_ratio_to_cores`、`concurrent_threads_soft_limit_*`）允许最多`max_concurrent_queries`个查询进入，但只有几十个线程在执行——查询排队数分钟；以及把大量插入流切换到`async_insert`且`wait_for_async_insert = 1`，在拥有数十个MV的表上，同步flush会同时执行数千个MV子查询。
**验证：**峰值前后每小时的`query_log_details` `count`与`avg duration`；`configuration/`中的`max_concurrent_queries`、workload/profile；`metric_log`的池相关列。
**修复：**处理慢的根源；只有在有余量时才提高`max_concurrent_queries`；按用户`CREATE QUOTA`或`max_concurrent_queries_for_user`；表级`max_concurrent_queries`；审视workload线程上限（优先级是绝对的，不是按比例的）。
**公开参考：**文档 → `max_concurrent_queries`、“Workload scheduling”、“Quotas”。

### P-21超时：服务端限制、客户端取消、分布式DDL
**你看到：**159 `TIMEOUT_EXCEEDED`（消息为`max_execution_time`、"Watching task /clickhouse/task_queue/ddl/… is executing longer than distributed_ddl_task_timeout" 或 "Timeout exceeded while reading from socket"）；394 `QUERY_WAS_CANCELLED`；服务端出现1002/210 "Broken pipe"，同时客户端报告读取超时；`ON CLUSTER` DDL耗时180秒后失败。
**通常意味着：**(a) 查询确实超过`max_execution_time`；(b) 客户端socket超时短于查询（客户端取消，服务端记录394/210）——常见于`OPTIMIZE … FINAL`、大`INSERT SELECT`、大表`ALTER`；(c) `ON CLUSTER`等待集群定义中的每台主机——一台宕机/改名的主机或陈旧的`system.clusters`条目会让每个DDL超时（检查`clusters.errors_count`，P-44）；(d) `DROP TABLE`因表引擎有外部消费者（Kafka）而挂起，或等待锁（Join/Buffer表在并发插入时出现`DEADLOCK_AVOIDED`）。
**验证：**失败hash的`query_duration_ms`对比其成功运行；`clusters`；该query id的`text_log`。
**修复：**将客户端`receive/send timeout`与合理时长对齐，或异步执行长DDL/`OPTIMIZE`；`distributed_ddl_task_timeout`/`distributed_ddl_output_mode = 'none_only_active'`；修复集群定义；当其他查询占着表时，用`lock_acquire_timeout`重试`TRUNCATE`/`DROP`。
**公开参考：**文档 → `max_execution_time`、`distributed_ddl_task_timeout`、“Distributed DDL”。

### P-22线程耗尽症状
**你看到：**`errors`/`text_log`中439 `CANNOT_SCHEDULE_TASK`、460 `CANNOT_CREATE_TIMER`、1000 "No thread available"；`host_info.cpu.load_avg` ≫ 核心数；有时CPU时间持平而并发查询上升（锁竞争）。
**通常意味着：**饱和的下游症状——并发查询突发、失控的后台任务、数千个MV子查询，或某版本锁持有时间增加。这个错误是时间锚点，不是原因。
**验证：**当时是什么消耗了线程：按user/hash的`query_log_details` count、`part_log` merge、`metric_log`池。
**修复：**先处理消耗者；之后才考虑`max_thread_pool_size`/`thread_pool_queue_size`。如果升级后CPU持平而并发和时长上升，按P-54处理。
**公开参考：**文档 → 服务器设置`max_thread_pool_size`、`max_concurrent_queries`。

## 缺陷、损坏、schema

### P-30某一版本上反复崩溃或LOGICAL_ERROR
**你看到：**`system.crash_log`行（signal 11 SIGSEGV / 6 SIGABRT）具有相同的顶部栈帧；`logs/`的`<Fatal> BaseDaemon`块；`system.errors`中49 `LOGICAL_ERROR`或1001 `STD_EXCEPTION`（`std::out_of_range`、`unordered_map::at`）在同一代码路径上出现数百次（常是带`JSON`/`Object`/`Nullable`的表在输出格式/projection中的merge）；每次崩溃前有相同的query hash或作业。
**通常意味着：**内部不变量失败——该构建上的缺陷，通常由特定功能触发（JSON/Variant/Dynamic列、其上的projection、带行过滤MV的插入去重token、新的优化器路径）。有些已在同一minor的后续补丁中修复。
**验证：**`version`；崩溃是否跟随确定性触发（客户作业、`MATERIALIZE PROJECTION`、某个ALTER）；`crash_log.query_id` → `query_log_details`；然后`clickhouse-source.md` §3–4（客户补丁到同一minor最新补丁之间的发布说明；按确切消息搜索issue）。
**修复：**收集**所有** `<Fatal>`块（不只是头部）和涉事表的DDL；如果后续补丁已有修复，先在同一minor内升级；见过的绕过方法：删除JSON列上的projection、避免触发功能、回滚补丁。没有`clickhouse-source.md` §4的协议时，绝不要把它说成“已知bug”。注意，非常旧的补丁（某minor的`.1`/`.2`）不包含该minor的任何缺陷修复。
**公开参考：**GitHub `ClickHouse/ClickHouse`中标记为`bug`/`crash`的issue；每个补丁的发布说明。

### P-31 part损坏/不可读：数据损坏或版本错配
**你看到：**`system.detached_parts.reason IN ('broken','broken-on-start','covered-by-broken','unexpected')`（detached part，分离part）；`errors`中40 `CHECKSUM_DOESNT_MATCH`、226 `NO_FILE_IN_DATA_PART`（"No columns.txt"）、33 `CANNOT_READ_ALL_DATA`、读取列时128/131 "Too large array/string size"；日志出现 "N parts broken and need manual correction"、"Initialization failed, table will remain readonly … rows in filesystem are suspicious"。
**通常意味着：**(a) 真正的损坏——磁盘错误、写入中断、硬件问题；(b) **版本错配**——更新版本的服务器写入的part（新序列化：sparse columns、packed parts、新的Variant/JSON磁盘格式、兼容性驱动的MergeTree默认值）被旧版本读取，典型于滚动升级中途，或从更新/Cloud构建降级/恢复到OSS后；(c) 环境问题伪装成损坏——例如重启时到对象存储的DNS失败表现为“corrupt file”。
**验证：**每台主机的版本（`system.clusters`、日志横幅）与升级时间线；“broken”part是否都来自较新的节点；当时对象存储的可达性。
**修复：**对 (b)，完成升级而不是用旧二进制读取；降级前设置与`compatibility`相关的设置（例如降到23.8以下前设`ratio_of_defaults_for_sparse_serialization = 1.0`）；永远不要在一个仓库中混用不同二进制；对 (a)，使用`force_restore_data`标志 / `SYSTEM RESTORE REPLICA`，从健康replica重新获取，只有在确认另一个replica有数据后才丢弃`broken*` detached part。
**公开参考：**文档 → “Self-managed upgrade”（兼容性、降级说明）、`system.detached_parts`。

### P-32 MV、源表与目标表之间的schema漂移
**你看到：**`query_log_details`中`Insert`的`tables` = 某个MV目标，出现53/70 `TYPE_MISMATCH`/`CANNOT_CONVERT_TYPE`、349 `CANNOT_INSERT_NULL_IN_ORDINARY_COLUMN`、117 `INCORRECT_DATA`、10 `NOT_FOUND_COLUMN_IN_BLOCK`；`part_log`的`MutatePart`/`MergeParts`以相同错误码失败；原本廉价的查询CPU达到100%，因为MV列类型与目标不同（例如`FixedString(32)` vs `String`）。
**通常意味着：**DDL以错误顺序或不同类型应用到源表、MV和目标表；ALTER提交期间按旧schema写入了part；JSON路径类型提示比传入数据更严格。
**验证：**比较源表、MV（`as_select`）和目标表的`create_table_query`/`system.columns`；mutation `command`时间戳。
**修复：**对齐类型；在链上变更类型时，暂停插入，按下游→上游一致地alter，`MODIFY QUERY` MV，再恢复；对于新类型下不可读的part：`DETACH PARTITION`取消merge，修复后重新attach；把列设为`Nullable`/放宽提示，而不是拒绝数据。
**公开参考：**文档 → “Materialized views”（schema变更）、`ALTER TABLE MODIFY QUERY`。

### P-33 INSERT“成功”但行缺失、重复或错误
**你看到：**`query_log_details`的`Insert`中`written_rows = 0`但没有错误；`text_log`出现 "Deduplication path already exists" / 389 `INSERT_WAS_DEDUPLICATED`；`asynchronous_insert_log`出现`FlushError`而客户端看到OK（`wait_for_async_insert = 0`）；源表在增长而MV目标为空（`query_views_log_3_days`：`zero_write_executions` ≈ `executions`且`read_rows > 0`——通常是插入用户上的 **row policy**；见P-35）；使用`insert_quorum`/async重试后出现重复行；旧版本上`async_insert` + 参数化`INSERT … VALUES ({p:Type})`导致列值在行间错乱。
**通常意味着：**(a) 块去重：相同的块（或同一个`insert_deduplication_token`被复用于不同负载）被静默跳过；(b) fire-and-forget异步插入在flush失败时丢数据；(c) `ROW POLICY`限制了插入用户从源表*读取*的内容，于是MV什么也没写；(d) 异步插入模板处理中的特定版本缺陷（上游已修复；检查客户版本）。
**验证：**`system.tables`策略不在bundle中——请求`system.row_policies`；`configuration/users.d`中插入用户的`Settings`；`asynchronous_insert_log.status`；版本。
**修复：**当丢失不可接受时设`wait_for_async_insert = 1`；为每个负载使用唯一token或去掉`insert_deduplication_token`；给插入用户row policy豁免；升级以修复模板缺陷。
**公开参考：**文档 → “Asynchronous inserts”、`insert_deduplicate`、`insert_deduplication_token`、“Row policies”。

### P-34 MV链放大每次INSERT
**你看到：**`system.tables`：一个源表上有5–80个`MaterializedView`（统计`dependencies_table`），存在两跳链，MV带`JOIN`/`ARRAY JOIN`/`GROUP BY`写入普通`MergeTree`；`part_log`中MV目标每小时的`NewPart` = N × 源插入数；TOO_MANY_PARTS报 "while pushing to view …"；`asynchronous_insert_log.p90_flush_ms`以秒计；241 "while pushing to view"（P-10）；`query_views_log_3_days`直接显示每个视图的成本——对一个源表上所有视图的`max_view_duration_ms`和`median_peak_memory_usage`求和。
**通常意味着：**每个插入块都被每个MV同步处理（并按分区、按projection）：每个MV目标`S3 PUTs ≈ 4 × 分区数 × (1 + projections)`；异步flush要等最慢的MV；MV带`GROUP BY`写入非聚合引擎会产生重复键；级联MV看到的是**原始插入块**，而不是合并后的状态。
**验证：**每个源表的MV数量；目标引擎；插入频率（P-02）。
**修复：**先批量插入；每个源表限制MV数量（约10个），用`ARRAY JOIN`多粒度MV合并；`GROUP BY` MV使用`AggregatingMergeTree`/`SummingMergeTree`目标；MV内用`dictGet`而不是`JOIN`；对重度分区的目标用`Null`引擎暂存；用`min_insert_block_size_*_for_materialized_views`限制内存。
**公开参考：**文档 → “Materialized views”最佳实践、“Cascading materialized views”。

### P-35 MV运行正常却不写入任何数据
**你看到：**`query_views_log_3_days`中`status = 'QueryFinish'`、`exception_code = 0`、`read_rows > 0`、`written_rows = 0`——某个视图的`zero_write_executions`接近`executions`，而同一源表上的其他视图仍在写入；源表在增长而MV目标停止增长；在链中，某一跳每小时的`written_rows`与上一跳背离。
**通常意味着：**(a) MV的SELECT合法地过滤掉了所有内容——先检查其WHERE，这是常见情况；(b) MV内的JOIN右表在块被处理时为空，通常是因为右表读取的是正在被交换（`EXCHANGE`/`RENAME`）的表或上游MV自己的目标，于是INNER JOIN什么也不产出；(c) 插入用户上的`ROW POLICY`限制了MV能从源表读取的内容（P-33c）；(d) 插入的并行处理没有squash，子查询看不到正在插入的块。
**验证：**`system.tables`中该MV的`as_select`；被join的表是否由可刷新MV或`EXCHANGE TABLES`重建（受影响分钟附近的`text_log`）；`system.row_policies`不在bundle中——请求它；比较该视图在事故前后分桶中的`zero_write_executions`比例。
**修复：**在摄入路径上通过字典而不是JOIN读取被交换或刷新的表；避免JOIN右表读取自身写入目标的MV；给插入用户授予（grant）row policy豁免。`materialized_views_squash_parallel_inserts`会改变 (d) 中的行为——把它当作在客户版本上测试的假设，而不是处方。
**不要混淆：**P-33（插入本身没有写入任何内容）、P-36（MV抛异常了——这里没有）。

### P-36基础part提交后MV推送抛异常
**你看到：**`query_views_log_3_days`中`status = 'ExceptionWhileProcessing'`且`exception_code`非零的行，结尾是`… while pushing to view db.mv`。两种形态：**默认**——`query_log_details`中的父INSERT也以该消息失败，且管道中*每个*视图都有一行`ExceptionWhileProcessing`，文本相同（一次失败的INSERT，按每个视图各出现一次——罪魁是点名的视图，其行数就是失败次数）；**`materialized_views_ignore_errors = 1`**——INSERT为`QueryFinish`，只有抛异常的视图有该行，`text_log`说 "Cannot push to the storage. Error is ignored because the setting materialized_views_ignore_errors is enabled"。两种情况下，源表有这些行而MV目标没有；失败行上的`written_rows`统计的是已回滚的行。
**通常意味着：**基础插入和MV推送不是原子的——part先提交，然后每个视图依次运行。常见错误码：60 `UNKNOWN_TABLE`（MV读取的表在插入中途被交换、重命名或detach——例如可刷新MV执行`EXCHANGE TABLES`，或维护期间DETACH/ATTACH）、241（P-10/P-34）、MV目标上的252（P-01）、关闭或节点drain期间的341 `UNFINISHED`。
**验证：**`exception`文本会点名失败的对象**以及正在推送到的视图**——这个归属很重要，因为在默认设置下，一次失败的INSERT会为管道中*每个*视图写一行`ExceptionWhileProcessing`，消息相同，所以跨视图计数会把一次失败乘以扇出；两个无关的损坏视图会点名两个不同的罪魁；同一分钟的`text_log`中找EXCHANGE/RENAME/shutdown行；是否设置了`materialized_views_ignore_errors`，这会让这些失败变成无错误到达客户端的丢失；同一小时源表与目标表的`part_log` NewPart。
**修复：**让被交换的表远离同步摄入路径；使客户端批次幂等且可重试；把刷新安排到摄入高峰之外；241/252见P-34/P-01。
**不要混淆：**P-35（完全没有异常）、P-33（基础插入本身失败或被去重）。

## 复制、Keeper、网络、存储

### P-40 Keeper会话丢失 → replica只读
**你看到：**`system.replicas.is_readonly = 1` / `is_session_expired = 1`；`errors`/`query_log_details`中成批的999 `KEEPER_EXCEPTION`（`Session expired`、`Connection loss`、`Operation timeout`）和242 `TABLE_IS_READ_ONLY`；`metric_log.zk_hw_exceptions > 0`，`zk_transactions`激增；`text_log`中出现`ZooKeeperClient`/`ReplicatedMergeTreeRestartingThread`消息；INSERT失败，DDL和merge停滞；Keeper重启期间`select_sequential_consistency = 1`的查询失败。
**通常意味着：**Keeper（或ZooKeeper）不可用、缓慢或正在重启：网络分区、Keeper磁盘饱和（fsync延迟）、Keeper规格不足（内存/CPU、ARM/小容量卷）、leader重选风暴、Keeper重启/扩缩事件。重连后replica通常能恢复；在某些旧版本上，表会一直只读直到服务器重启。
**验证：**将`zk_hw_exceptions`、999计数和`is_readonly`做时间关联；`configuration/`中的Keeper主机（奇数quorum？`session_timeout_ms`？）；读取`keeper/<host>_<port>.txt`（`zk_server_state`、`zk_outstanding_requests`、`zk_avg_latency`，自v0.7起在onprem/gov采集——cloud中除非`-keeper-mntr on`否则缺失）；请求Keeper日志，它不在bundle中。
**修复：**先稳定Keeper（专用快速磁盘、x86节点、≥ 3个节点、不要与重型ClickHouse I/O同机）；限流INSERT（饱和期间不要增加写入者）；故障后`SYSTEM RESTART REPLICA` / `SYSTEM RESTORE REPLICA`；在非常长时间运行的高事务部署上使用`use_xid_64 = 1`（32位xid溢出会让长BACKUP查询挂起）；针对已知重选修复升级Keeper。
**公开参考：**文档 → “ClickHouse Keeper”（配置、four-letter command（四字命令））、`system.replicas`、`SYSTEM RESTORE REPLICA`。

### P-41 replication queue卡住 / absolute_delay过大
**你看到：**`system.replicas.absolute_delay`以分钟到小时计，`queue_size`/`inserts_in_queue`很高且稳定；`replication_queue`中有许多`GET_PART`（受fetch限制）或`MERGE_PARTS`（受merge限制）条目，`num_tries`达到数百，`postpone_reason`为 "Not executing fetch of part … because N fetches already executing, max N"，`last_exception`为 "No active replica has part … or covering part"（234）、"No part … in table"（232），fetch错误210/209；日志 "Will mimic replica …"（日志指针丢失——`max_replicated_logs_to_keep`太小）。
**通常意味着：**fetch池相对于摄入速率太小（`background_fetches_pool_size`，默认16，旧版本8）；源replica不再拥有该part（已被删除/合并掉；陈旧replica条目 → P-43）；interserver连接/DNS错误（P-42/P-44）；某个replica离线太久，其日志条目已被轮转。replica之间的merge不均衡无害，除非某个replica出现*part数量积压*。
**验证：**`GROUP BY type`计数；最旧的`create_time`；`active_replicas < total_replicas`（`replicas_problem_tables`）；`clusters.errors_count`。
**修复：**提高`background_fetches_pool_size`（现代版本为服务器级）；修复interserver host/DNS；`SYSTEM RESTART REPLICA`/`RESTORE REPLICA`；滚动升级前提高`max_replicated_logs_to_keep`，以免落后的replica丢失指针；从Keeper中删除真正死亡的replica（`SYSTEM DROP REPLICA`）。
**公开参考：**文档 → `system.replication_queue`、`SYSTEM DROP REPLICA`、`background_fetches_pool_size`、`max_replicated_logs_to_keep`。

### P-42拓扑与配置错误
**你看到：**`configuration/`中主机没有`<interserver_http_host>`，而主机名解析有歧义（fetch发到错误集群 → `replication_queue`中出现232/234）；`<macros>` `{replica}`重复（224 `REPLICA_IS_ALREADY_ACTIVE`）；Keeper主机数为偶数或只有一个；`insert_quorum`大于replica数，导致Distributed插入永远排队（`SYSTEM FLUSH DISTRIBUTED`上出现285）；`Replicated`数据库中的新节点只收到它加入后创建的表；`<remote_servers>`列出已下线主机（`ON CLUSTER`超时，P-21）。
**通常意味着：**配置中的拓扑与现实不符。
**验证：**将`system.clusters`（host_name/host_address/is_local/errors_count）与`<remote_servers>`和`<macros>`交叉核对；`system.replicas.total_replicas`对比服务器数量。
**修复：**显式设置`<interserver_http_host>`（对等节点可达的FQDN）；宏唯一；3/5个Keeper节点；从集群定义中移除死亡主机，或使用`skip_unavailable_shards`；对晚加入的`Replicated`数据库replica，用`CREATE TABLE … AS` + `SYSTEM SYNC REPLICA`重建表（或重建数据库replica）。
**公开参考：**文档 → “Replication”（`interserver_http_host`、宏）、“Distributed DDL”、`Replicated`数据库引擎。

### P-43 Keeper元数据陈旧
**你看到：**`system.replicas_problem_tables.total_replicas` > 实际存在的服务器数（该文件包含问题表的Keeper支撑列以及一个健康样本）；在已有路径上重建表时`errors`出现253 `REPLICA_ALREADY_EXISTS`，244/308意外Keeper节点，恢复后日志中出现142 `NOT_FOUND_NODE`；某个replica上mutation永不完成且无错误（非常旧的block编号节点）；日志循环引用一个已不存在表的UUID（失败的`RESTORE`后留下的孤立目录项）。
**通常意味着：**replica被删除或服务器被重建时没有执行`SYSTEM DROP REPLICA`；`RESTORE`复用了Keeper路径；多年前操作留下的残留。
**验证：**Keeper已知的replica列表对比真实主机（请求`SELECT * FROM system.zookeeper WHERE path = '<table_path>/replicas'`）；`SHOW CREATE`中的路径。
**修复：**对幽灵replica执行`SYSTEM DROP REPLICA '<name>' FROM TABLE db.t`；恢复到新路径/数据库名（`RESTORE … AS`），或按文档使用`allow_different_table_def`/`restore`设置；只有在工程指导下才删除孤立节点。
**公开参考：**文档 → `SYSTEM DROP REPLICA`、“Backup and restore”（复制表）。

### P-44网络、DNS与interserver故障
**你看到：**`system.clusters.errors_count > 0` / `estimated_recovery_time`；198 `DNS_ERROR`、209/210 socket超时/重置、279 `ALL_CONNECTION_TRIES_FAILED`、519 `NO_REMOTE_SHARD_AVAILABLE`、574待处理Distributed字节；`part_log`的`DownloadPart`带`error != 0`；日志提到`/etc/hosts`中DNS无法解析的主机；HTTP代理后的native协议客户端健康检查失败。
**通常意味着：**节点间连接问题（防火墙、MTU、重启后的DNS缓存、不支持native协议的代理），或存储与服务器之间的网络链路饱和（所有查询变慢、S3读取等待上升，P-45）。
**验证：**错误消息中出现了哪些主机；错误是否对称（两个方向）以及是否与重启相关（DNS缓存）。
**修复：**为每个节点主机名配置DNS条目；`<interserver_http_host>`；keep-alive；HTTP接口放在L7代理后；用`skip_unavailable_shards`/`connections_with_failover_max_tries`提高韧性；保证存储网络容量。
**公开参考：**文档 → `system.clusters`、“Distributed table engine”设置。

### P-45对象存储（S3/Azure/MinIO）限流、延迟、迁移与磁盘tier（分层）
**你看到：**499 `S3_ERROR`带`SlowDown`/`TooManyRequests`/`Reduce request rate`/`AccessDenied`；`MOVE PARTITION … TO DISK`失败，重试时出现84 `DIRECTORY_ALREADY_EXISTS`（残留`moving/`目录）；热盘接近满时数据意外落到冷/外部磁盘（`system.parts.disk_name`）（`move_factor`默认0.1）；JBOD磁盘填充不均；merge因S3读取错误停滞；对巨大S3表的查询在读取前固定花约2分钟（数千个part的每part读池建立）；备份卡住数天；桶中的对象数量远超`system.remote_data_paths`所知（崩溃/上传失败后的孤立blob）。
**通常意味着：**某个前缀的请求速率限制、升级后凭据/策略变化、存储策略卷填到迁移阈值，或part/mark太多，使对象存储往返成本过高。
**验证：**每个卷的`system.disks.type`和`free_pct`；`part_log`的`MovePart`错误；按小时的`text_log` S3消息；S3后端表的part数。
**修复：**退避/重试设置（`s3_max_*`、`s3_retry_attempts`），分散前缀，修复桶策略；提高对`move_factor`的认识（如果希望手动分层就降低它）；在期望快速读取前先把part合并下去（P-01）；启用`system.blob_storage_log`和为未完成multipart上传设置生命周期规则；用`system.remote_data_paths`对比桶列表来调查孤立对象。
**公开参考：**文档 → “External disks / S3”、“Storage policies”（`move_factor`）、`system.blob_storage_log`、`system.remote_data_paths`。

## 查询性能与schema设计

### P-50会使裁剪失效的ORDER BY / 分区设计
**你看到：**`system.tables.sorting_key`以高基数列为开头（UUID、trace id、timestamp）或为`tuple()`；`query_analysis/text_log_parts`显示 "Selected N/N parts by partition key, … marks"——part未被裁剪而granule被裁剪；有主键等值条件时查询仍慢；压缩比差（HC-4.5）；`query_log_details`中有`SELECT *`样本。
**通常意味着：**当每个part都覆盖完整键范围时（按时间批量插入 + `ORDER BY (id, ts)`），主索引无法跳过整个part，或首列选择性太高而无法压缩；分区没有发挥索引作用（P-03）。
**验证：**将`sorting_key`与top hash的WHERE形态对比；每张表的`data_compressed_bytes`对比`data_uncompressed_bytes`；`partition_key`。
**修复：**按基数从低到高排列列，把最常用的过滤前缀放在最前；查询按时间限定时，把时间桶放在id之前；对 < 1万个不同值使用`LowCardinality(String)`（超过10万避免）；键中避免`Nullable`；为次要访问路径使用projection或skip index；`SELECT`只取需要的列。
**公开参考：**文档 → “Choosing a primary key”、“Sparse primary indexes”指南、“Skip indexes”。

### P-51在Replacing/Collapsing表上使用FINAL
**你看到：**`query`样本在大型`ReplacingMergeTree`/`CollapsingMergeTree`表上使用`FINAL`；即使`LIMIT`很小，`read_rows/count` ≈ 表行数；内存高；升级后`FINAL`偶尔返回空结果（特定版本缺陷——检查发布说明）。
**通常意味着：**`FINAL`必须在`LIMIT`之前读取并去重所有行；`FINAL`下不会使用projection；PREWHERE自动移动需要`optimize_move_to_prewhere_if_final = 1`。
**验证：**将FINAL hash的`read_rows`与`system.parts`中的`sum(rows)`对比；表的part数（part越多工作越多）。
**修复：**配合能限定重复范围的分区键使用`do_not_merge_across_partitions_select_final = 1`；用`argMax`/`GROUP BY`去重模式；为查询专门建一个按需排序的MV；用`min_age_to_force_merge_seconds`（+ `_on_partition_only`）保持表已合并；在安静窗口执行`OPTIMIZE … FINAL`。
**公开参考：**文档 → “FINAL modifier”、ReplacingMergeTree。

### P-52 GROUP BY / JOIN内存与join构建侧
**你看到：**消息中出现241且带`AggregatingTransform`（GROUP BY）或`FillingRightJoinSide`/`HashJoin`（JOIN）；同一hash间歇性OOM（成功运行用100 MB，失败运行用20 GB）——统计信息缺失时join顺序优化器选择了大的构建侧；对数十亿行做聚合的`INSERT … SELECT`失败；高基数列上的`uniqExact`/`COUNT(DISTINCT)`。
**通常意味着：**聚合哈希表或JOIN右侧放不下；`max_insert_block_size`不适用于`INSERT SELECT`。
**验证：**查询样本；`configuration/users.d`中的`Settings`；版本（join顺序优化器行为在25.x–26.x间有变化）。
**修复：**`max_bytes_before_external_group_by`/`max_bytes_ratio_before_external_group_by`（溢写；保持`max_memory_usage` ≈ 2×）；减小右侧或使用`dictGet`；`join_algorithm = 'grace_hash'|'partial_merge'`、`max_bytes_in_join`；优化器判断错误时固定join顺序（`query_plan_optimize_join_order_limit = 0`或显式顺序）；用`uniq`/`uniqCombined64`代替精确去重；按key范围拆分`INSERT SELECT`；对长字符串GROUP BY键做哈希。
**公开参考：**文档 → “GROUP BY in external memory”、“JOIN”（算法）、“Memory limit exceeded” KB。

### P-53索引未生效与analyzer行为变化
**你看到：**DDL中有`tokenbf_v1`索引，但查询使用`LIKE '%substring%'`（token索引帮不上忙；需要`hasToken`或`ngrambf_v1`）；有多个skip index，但条件跨列使用`OR`（需要复合索引）；`x IN arrayMap(...)`/复杂表达式没有使用主键；升级后：`CYCLIC_ALIASES`（174）、`COLUMNS()`顺序变化、`lambda()`旧语法、`Unknown expression identifier`——新analyzer的行为差异（自24.3起默认）。
**通常意味着：**索引类型与谓词不匹配，或analyzer改变了语义。
**验证：**DDL中的`INDEX`定义对比查询样本；版本；`text_log_parts`的granule计数。
**修复：**子串用`ngrambf_v1`，token用`hasToken*`，`OR`用复合索引；重写常量数组表达式；作为诊断，用`enable_analyzer = 0`（≤ 24.x：`allow_experimental_analyzer = 0`）运行查询以确认是analyzer相关变化，然后改写查询而不是保留旧路径。
**公开参考：**文档 → “Data skipping indexes”、“Analyzer”（行为变化）。

### P-54升级后的性能回退
**你看到：**`logs/`显示版本变化；从那个小时起，*同一* `normalized_query_hash`的`query_log_details` `query_duration_ms/count`或`memory_usage/count`跳升（×1.5–×2），241/202/159增多，`part_log` merge时长变化，CPU持平而并发上升（锁竞争），`text_log`量爆炸（某日志行出现频率高得多），`system.tables`读取变慢（目录行为变化），PK裁剪被视图/row policy丢失，轻量更新patch part禁用了某项优化；有时只有*混合版本*窗口内表现差（滚动升级，replica处于两个版本）。
**通常意味着：**真正的版本回退、默认值变化（`compatibility`设置），或预期的过渡成本（重启后冷缓存、混合版本、按新格式重新merge）。
**验证：**对同一hash做ProfileEvents前后对比（如果窗口横跨两者，用`query_analysis/profile_events_compare`）；查看这些版本的`SettingsChangesHistory`；两个版本之间的发布说明（`clickhouse-source.md` §3）；所有节点是否同一版本。
**修复：**完成推广，不留混合版本窗口；调查时设置`compatibility = '<old version>'`恢复旧默认值（注意它也会切换需要重启的MergeTree默认值）；如果回退确认且阻塞，在支持窗口内回滚；用两个版本的ProfileEvents diff报告。优先选择LTS minor（`YY.3`、`YY.8`）和所选minor的最新补丁。
**公开参考：**文档 → “Self-managed upgrade”、`compatibility`设置、changelog的 “Backward Incompatible Change” 段。

### P-55监控与内部工具主导查询负载
**你看到：**按`user`的`query_log_details`：监控/exporter/scraper用户拥有最多`count`，有时还有最大的`memory_usage`；模式如每隔几秒在数万张表的目录上执行`SELECT * FROM system.tables`、`DESCRIBE`、`system.parts`扫描；`system.metric_log`的merge失败（P-17）；因为监控占着内存，“小”用户查询出现241。
**通常意味着：**可观测性工具相对于目录规模抓取过于频繁或范围过广；某些版本让`system.tables`读取更昂贵（例如计算`total_rows`）。
**验证：**每个用户的`count`和`memory_usage/count`；top内部hash的`query`样本。
**修复：**降低抓取频率，只选需要的列/表，在巨大目录上避免对`system.tables`/`system.columns`使用`SELECT *`，用`system.metrics`/`system.asynchronous_metrics`而不是扫描日志表；给监控用户设置内存/profile限制。
**公开参考：**文档 → “Monitoring”（Prometheus endpoint）、`system.asynchronous_metrics`。

### P-56分布式加载（`s3Cluster`、`INSERT … SELECT`）慢——shard、文件倾斜、发起端插入
**你看到：**某个“慢SELECT”hash的`query_details.tables`为`_table_function.s3Cluster`（或`s3`、`url`、`remote`）且`is_initial_query = 0`；同一分钟内在不同replica上有多次执行，`read_rows`与`query_duration_ms`成比例；`text_log_full`显示`Reading object …`行，文件大小非常不均，然后长时间静默直到`Source finished: files_read=N`；`profile_events`中`NetworkSendElapsedMicroseconds`占`RealTimeMicroseconds`的大部分，`ParquetFetchWaitTimeMicroseconds`很大，`OSCPUWaitMicroseconds` ≈ `OSCPUVirtualTimeMicroseconds`；`part_log`显示目标表所有`NewPart`事件都落在一个replica（`hostname`）上，并向其他replica扇出`DownloadPart`。
**通常意味着：**该语句是`INSERT INTO t SELECT * FROM s3Cluster(...)`。每个replica读取*文件*的一个子集，并把解码后的行流式送回**发起端**，由发起端完成所有插入；墙钟时间 = 最慢replica的份额，而份额按文件划分，因此少数大文件占主导。解码Parquet很耗CPU，在受限pod上表现为CPU wait。“快速”的执行只是拿到了小文件的那个replica。
**验证：**各replica的`read_rows/duration`大致相等（是倾斜，不是慢节点）；`Reading object`行中的文件大小；`part_log`中目标表的`NewPart`集中在一个`hostname`上。
**修复/缓解：**`SET parallel_distributed_insert_select = 2`，让每个replica在本地插入自己的份额（消除占据大部分墙钟时间的回传）；把源拆成许多大小相似的文件（≥ 3× replica数）以平衡文件分布；在服务未被CPU限制时执行批量加载，或在此期间扩容；CPU wait高时`max_insert_threads`/`max_threads`帮不上忙。对于一次性加载，如果数据集本来就只有几个文件，可考虑在单个replica上用普通`s3()`并设置`max_download_threads`。
**不要混淆：**P-21（同一语句的客户端超时）、P-52（内存——这里已跟踪内存很小，未跟踪的Arrow分配很大）、P-54（仅当同一加载在升级后变慢时）。
**公开参考：**文档 → `s3Cluster`表函数、`parallel_distributed_insert_select`、“Inserting data from S3”指南（文件布局与并行度）。

### P-57 Keeper quorum丢失或leader反复切换——会话过期级联
**你看到：**`metric_log_coordination_3_days` → `sum(ProfileEvent_ZooKeeperHardwareExceptions)`在一小时内从0跳到数百万，而`sum(ProfileEvent_ZooKeeperTransactions)`崩塌；`query_log_details` → 同一时段充满INSERT上的999 `Session expired`、319 `UNKNOWN_STATUS_OF_INSERT`，然后`CREATE OR REPLACE TABLE db.`.tmp.inner_id.<uuid>``上出现571 `DATABASE_REPLICATION_FAILED`和57 `Mapping for table with UUID … already exists`（`/* ddl_entry=query-N */`），最后最繁忙的插入目标上出现252 `TOO_MANY_PARTS`；`part_log_3_days` → 故障小时内的`MergeParts`带`error = 999`，随后是`NewPart > 0`且`MergeParts = 0`的小时；`system.errors` → 221 `NO_SUCH_INTERSERVER_IO_ENDPOINT`和86 `RECEIVED_ERROR_FROM_REMOTE_IO_SERVER`达到数万次；`zookeeper_connection` → 已运行数月的服务器上`session_uptime_elapsed_seconds`只有几分钟；`zookeeper_log_errors_1_day` → 每小时`sessions` > 2，`error = ZSESSIONEXPIRED` / `ZCONNECTIONLOSS`。
**通常意味着：**Keeper集群失去quorum或不断重选leader（某个Keeper节点内存或磁盘耗尽、五个成员中两个无法同步、强制领导权转移、网络分区）。所有ClickHouse会话同时过期；对复制/共享表的INSERT无法提交；通过Keeper分配的merge停止，于是part堆积到`parts_to_throw_insert`；会话恢复后`Replicated`数据库重放其DDL日志，与自身部分应用的`CREATE`冲突（57），若会话在重放中途死亡则直接失败（571）。interserver 221/86风暴是replica在重启后重新发现彼此endpoint，除非持续存在，否则属于恢复噪声。
**验证：**相关小时的Keeper健康检查（health-checks HC-3.8）判定为*不可用*，而不是*短暂抖动*（异常 > 1000/小时**且**事务 < 7天中位数的50%）；metric_log、part_log错误和query_log异常*三者*的小时对齐（仅一个来源可能只是单张表）；`system.databases`的`Replicated`数量和`system.tables`数量给出Keeper负载倍数（数百个Replicated数据库 × 数十个replica = 非常大的watch/会话集合）；`configuration/zookeeper.xml`列出集群（奇数？5个？）；`text_log_histogram`每小时`DDLWorker`/`DatabaseReplicated`/`ZooKeeperClient`的错误计数。Keeper侧确认需要Keeper日志和每个成员的`echo mntr | nc <keeper> <port>`——bundle无法展示这些。
**修复/缓解：**先稳定集群（恢复奇数、同步的quorum；五成员集群中两个无法同步时，应先恢复到三个健康成员再添加新成员；为快照 + 日志配置Keeper内存并设置`max_memory_usage_soft_limit`）；然后让ClickHouse恢复——对仍只读的表执行`SYSTEM RESTART REPLICA`，对会话从未重建的服务器执行滚动重启；检查`distributed_ddl_queue`后才重跑失败的DDL。之后缩小影响面：减少每个Keeper的`Replicated`数据库数量（或每个集群使用专用集群）、`insert_keeper_max_retries`/`insert_keeper_retry_*`让INSERT挺过短暂丢失，并在`zk_followers`/`zk_synced_followers`和leader变更上设置Keeper告警。
**不要混淆：**P-40（单个replica的会话丢失——这里每个replica上的每张表同时受影响）、P-01（Keeper健康时的merge积压——这里merge是*缺失*，不是慢）、P-42（永久配置错误的集群——这里在事故前一直正常）。
**公开参考：**文档 → ClickHouse Keeper（quorum、`four_letter_word_white_list`、`mntr`）、`insert_keeper_max_retries`、Replicated数据库引擎、`SYSTEM RESTART REPLICA`；提到 "Session expired" 风暴的ClickHouse/ClickHouse issue很常见——只有在走完`clickhouse-source.md`的四步协议后才引用。

### P-58事故之后：Active part上出现107 FILE_DOESNT_EXIST / `The specified key does not exist`
**你看到：**`query_log_details` → 在Keeper恢复**之后**的数小时内，许多表出现107 `File data/<uuid>/<part>/<file> doesn't exist … attempt to read data part <part> (state Active) failed … Please retry the query`，或`The specified key does not exist. This error happened for S3 disk … while reading key: <bucket key>`；消息中的磁盘`system.disks.type = ObjectStorage`，且`storage_policies`将用户表放在其上（`s3_with_keeper` / `s3_plain_rewritable`磁盘，或覆盖其上的`cache`磁盘）；`system.metrics`（cloud模式，按replica）→ 大多数replica上`MetadataFromKeeperCacheObjects`达到数十万，少数replica上 ≈ 1；`blob_storage_log_7_days` → 缺失key在数小时或数天前有`Delete`，或有`failed = 1`的Upload；`part_log` → 故障期间出现84/504 `*_ALREADY_EXISTS`重试。
**通常意味着：**part的*元数据*与其 *blob* 不一致。在对象存储磁盘上，part是一组blob加一层元数据（本地元数据文件，或保存在Keeper中并带每服务器缓存的元数据）。Keeper故障后，服务器可能持有过时的元数据视图——指向另一replica上的merge已删除的blob，或指向被中断的提交从未写入的blob——对该part的每次读取都会失败，直到视图刷新。缓存被清空的服务器（那些 ≈ 1的replica）会惰性重建，在重建完成前也会遇到同样错误。此情形下真正损坏很少见；“please retry”的字面意思就是重试。
**验证：**107相关的表都位于对象存储策略上（join `system.tables.storage_policy`）；失败的文件种类是元数据类（`primary.idx`、`*.cmrk*`、`checksums.txt`），而不是某个part的某一列；`blob_storage_log`中该key有`Delete`（或由所有者运行`SELECT local_path, remote_path FROM system.remote_data_paths WHERE remote_path LIKE '%<key>%'`）；错误集中在缓存指标异常的replica上；*同一*查询在另一个replica上成功。
**修复/缓解：**刷新元数据视图而不是碰数据：对受影响表执行`SYSTEM RESTART REPLICA db.table`；在元数据由Keeper持有的磁盘上，在受影响服务器执行`SYSTEM DROP DISK METADATA CACHE <disk>`（然后确认缓存指标再次增长）；对仍然失败的服务器执行滚动重启；只有此后才考虑对在每个replica上都失败的part使用`CHECK TABLE` / `SYSTEM SYNC REPLICA` / detach-and-fetch。向ClickHouse报告时附上确切消息、磁盘类型和版本——这一类问题对版本敏感。
**不要混淆：**P-45（真正损坏的part被移到`detached/`并带`broken*`原因——这里part保持`Active`）、P-19（本地磁盘已满）、P-57（原因；此模式是其后果）、同一磁盘上的403/凭据错误（`system.events`的`S3*RequestsErrors`，消息中带`Forbidden`）。
**公开参考：**文档 → “External disks for storing data”（S3磁盘类型、元数据类型）、`SYSTEM DROP DISK METADATA CACHE`、`SYSTEM RESTART REPLICA`、`system.blob_storage_log`、`system.remote_data_paths`。

### P-59 SharedMergeTree replica不一致——后台池饱和导致part传播延迟
**你看到：**同一`SELECT count()`在某个replica上返回的行数比同组另一个少，持续数分钟到数小时，然后追上；cloud的`system.replicas`（v0.7：按`hostname`，问题行在前）→ 少数热表上`inserts_in_queue`达到数百到数千，`absolute_delay`以小时计，同一个`oldest_part_to_get`出现在多台主机上，`merges_in_queue` ≈ 0（HC-3.1的SharedMergeTree解读——在SMT上这些列统计的是**该replica尚未获取的level-0 part**，不是replication queue）；`metric_log_7_days` → 连续数小时`max_replica_avg_fetch_pool_tasks`达到或接近`background_fetches_pool_size`，和/或`max_replica_avg_schedule_pool_tasks`达到`background_schedule_pool_size`（告警（alert rule）`fetch_pool_saturated` / `schedule_pool_saturated`，HC-2.13/2.14）；`metric_log_by_host_3_days` → 每台主机的同样指标对比**实时**大小`max(CurrentMetric_BackgroundFetchesPoolSize)`，每小时`sum(ProfileEvent_SharedMergeTreeDataPartsFetchAttempt)`持平，而`sum(ProfileEvent_SharedMergeTreeSelectPartsFor*FetchParts)`高出数倍（HC-2.15）；`system.tables` → 每个replica上有数万个`Shared*MergeTree`表，其中很大一部分为空或不足100行（`total_rows`）；`system.merge_tree_settings` → `shared_merge_tree_parts_load_batch_size = 32`和`shared_merge_tree_leader_update_period_seconds = 30`为默认值（HC-2.16）；通常一次重启会让滞后的replica追上一阵子，随后在工作日呈波浪状再次滞后，周末缓解。
**通常意味着：**这是容量问题，不是故障。在SharedMergeTree上，replica通过Keeper得知新part，然后自己从对象存储*获取*其元数据，每张表每轮获取`shared_merge_tree_parts_load_batch_size`个part。fetch执行器正好有池大小个槽位且**没有队列**：找不到空闲槽位的part会被丢弃，并在下一轮重新选择，且没有日志行，因此一张想获取32个part的表只能拿到数万张竞争表之间的空闲槽位，其分配者随后退避。schedule池运行每张表的周期任务——*调度* fetch的part更新轮次、每表每周期一次的leader选举、清理——因此在数万张表时会先被钉满，而**schedule池被钉满会让fetch槽位空闲**（schedule池达到大小上限的主机显示fetch池低于其大小；schedule有余量的主机显示fetch池被钉满）。fetch策略优先选择未被待处理merge覆盖的part，因此最繁忙的表（其新part通常是merge源）**最后**恢复。重启会批量加载当前完整part集，绕过执行器——这就是它有效的原因，也是它只能维持到池再次填满的原因。
**验证：**(1) 池相关列对比*实时*大小——单节点用`server_settings`，集群用`metric_log_by_host_3_days`中每台主机的`max(CurrentMetric_Background*PoolSize)`——饱和必须持续数小时，而不是一个时刻；**绝不要假设是16**：Cloud服务的fetch池为64，自托管默认为16 / 512。(2) 头部`uptime:`——采集前重启过的节点会显示空池和干净的replicas文件，这什么也说明不了。(3) 上限测试：每台主机每小时的fetch尝试（或`part_log`的`DownloadPart`）在小时和主机之间波动在 ±10–20 % 内且 ≈ `pool_size × 3600 / 每次 fetch 秒数`（对象存储上每次fetch ≈ 0.5秒）——在上限处持平意味着满容量工作，不是卡住；停滞则显示零尝试而池仍满。(4) `part_log`的`DownloadPart`错误*不存在*（*失败*的fetch属于P-41 / P-44 / Keeper事故，不是此模式）。(5) 不是负载均衡bundle——但按前缀区分：头部`warnings:`行以`Warning:`开头（onprem/gov）会使比较失效，因为每张表都来自任意replica；而`Note:`形式（云服务的负载均衡endpoint）不会，因为那里的每replica文件会扇出，只有共享表来自应答的replica。
**修复/缓解：**先容量，后需求。提高`background_fetches_pool_size`和`background_schedule_pool_size`——两者都是`IncreaseOnly`服务器设置：配置重载可**无需重启**即时提高，只有降低才需要重启；16 → 128和512 → 1024在数万张表、每表数千part的积压上，第一个replica约40分钟清完。一次滚动几个replica：每个追赶中的节点会在接下来一小时内使其对象存储GET速率成倍增加，因此批间要观察对象存储首字节时间和Keeper ms/tx，并预期追赶时主机CPU和网络会很高。schedule池的每任务类型上限（≈ 0.8 × 大小）只在下次重启时采用新大小——计划在追赶*之后*滚动重启，而不是之前。然后降低需求：`shared_merge_tree_parts_load_batch_size` 32 → 64（每表每轮更多fetch）和`shared_merge_tree_leader_update_period_seconds` 30 → 300（选举/秒 ≈ Shared* 表数 / 周期；两者都是MergeTree设置——服务器`<merge_tree>`块，或按表`ALTER TABLE … MODIFY SETTING`，会在每个replica上生效）；删除或合并空表和近空表（每张仍会运行周期任务）；每表更少、更大的插入（要传播的part更少）。滞后存在期间，需要一致性的读取可使用`SETTINGS select_sequential_consistency = 1`（读取前同步part，额外的Keeper往返）或对单表执行`SYSTEM SYNC REPLICA db.table LIGHTWEIGHT`。重启只能批量加载一次。
**改动后，用同样的文件验证：**`metric_log_by_host_3_days`显示每台主机上的大小阶跃（重载的那个小时），fetch尝试先激增 ×3–5，然后回落至接近旧速率，而池现在远低于其大小；cloud的`system.replicas`中热表的`inserts_in_queue`每台主机 → 0（用`select_sequential_consistency = 1`重新检查count与本地值——当他处merge覆盖part时队列计数也会下降，所以0才是证明，下降不是）；merge池和`cpu_us`在获取的小part被merge时会在数小时内保持高位（`max_parts_active`先升后降），一旦进行中的merge低于`background_pool_size`就回到基线。
**不要混淆：**P-01（merge池饱和——`max_merge_pool_tasks`、part数量暴涨；这里merge池只在追赶期间忙）、P-41 / P-44（fetch在`part_log` / `replication_queue`中以Keeper或interserver错误*失败*——这里成功，只是太慢）、在onprem/gov采集的负载均衡bundle（头部`warnings:`行以`Warning:`为前缀——系统表来自任意replica，任何“滞后”都是哪个节点应答的假象；cloud模式的`Note:`形式是正常的Cloud endpoint，不会使每replica文件失效，这些文件会扇出）、普通`ReplicatedMergeTree`复制延迟（`system.replicas.absolute_delay`且`replication_queue`中有`GET_PART`条目，HC-3.x）、某个replica上卡住的mutation（P-14——现场最先尝试的就是kill mutation，结果并非原因）。
**公开参考：**文档 → “SharedMergeTree”、服务器设置`background_fetches_pool_size`、`background_schedule_pool_size`、`background_pool_size`；MergeTree设置`shared_merge_tree_parts_load_batch_size`、`shared_merge_tree_leader_update_period_seconds`；`select_sequential_consistency`；`SYSTEM SYNC REPLICA … LIGHTWEIGHT`；`system.metric_log`（`CurrentMetric_Background*PoolTask`、`CurrentMetric_Background*PoolSize`、`ProfileEvent_SharedMergeTree*`）、`system.server_settings`（`changeable_without_restart`）、`system.merge_tree_settings`、`system.replicas`。

### P-61 Keeper快照过大无法单块发送——成员落后时集群失去leader
**你看到：**`keeper/<member>.txt` → `mntr`的`zk_latest_snapshot_size`超过2 GiB（2 147 483 647字节），且`conf`的`snapshot_transfer_chunk_size=0`（HC-3.20）；某成员的`lgif`中`last_log_idx`低于leader的`first_log_idx`（HC-3.19）；成员返回`This instance is not currently serving requests`（HC-3.18）；在ClickHouse侧是P-57级联——所有会话过期，写入失败。
**通常意味着：**某成员离线时间超过保留日志覆盖范围——在大型集群滚动重启中很典型，加载数GB快照需要数分钟，而在写入负载下`reserved_log_items` + `snapshot_distance`条日志很快被绕过——因此它只能通过完整快照追赶。当`snapshot_transfer_chunk_size = 0`（默认）时，leader将整个快照作为一个对象发送；其大小在32位整数中求和，超过2 GiB后回绕，分配抛出`std::bad_alloc`。每个新leader都会在数秒内命中，于是领导权不断转移，集群最终没有leader。leader上的Keeper自身日志显示`Opening snapshot file … for chunked transfer`，然后`asio worker thread got exception: std::bad_alloc`、`snapshot install task for peer N timed out`和`will yield the leadership`。
**验证：**HC-3.19 / HC-3.20的发现会点名成员和大小；`zk_znode_count`和`zk_approximate_data_size`说明状态增长到多大；曾是leader的成员上的Keeper自身日志文件（工具未采集）包含`bad_alloc`行。
**修复/缓解：**保持落后成员**停止**；一起重启最新成员，使它们仅凭自己的日志形成quorum，无需任何快照传输。然后在**每个**成员上设置`snapshot_transfer_chunk_size`（例如`100000000`，100 MB——从几MB到1 GB均可）和`nuraft_snapshot_sync_ctx_timeout_ms`（例如`900000`，足够发送数GB快照）；两者都需要重启，因此先应用到服务中的成员（暂停写入时），然后才启动落后成员。在计划对大型Keeper滚动重启之前，先设置它们，并一次重启一个成员，等每个成员追上（`lgif`的`last_committed_log_idx` = `leader_committed_log_idx`）后再进行下一个。然后查明状态为何增长。
**不要混淆：**P-57（因内存、磁盘或网络丢失quorum——这里触发因素是某成员需要快照，而传输本身失败）；快照格式 < 7的32位顺序节点计数器（HC-3.20，`Sequential node counter … overflows int32`）——不同限制、不同消息。
**公开参考：**文档 → ClickHouse Keeper设置（`snapshot_transfer_chunk_size`、`nuraft_snapshot_sync_ctx_timeout_ms`、`reserved_log_items`、`snapshot_distance`、`write_snapshot_version`）和four-letter command（`lgif`、`conf`、`mntr`）。

### P-62 INSERT去重清理以remove淹没Keeper——多数返回ZNONODE
**你看到：**`aggregated_zookeeper_log_paths_1_day` → 某张表的`…/async_blocks`和`…/deduplication_hashes`占当天`removes`的大部分（HC-3.23）；`aggregated_zookeeper_log_by_hour_2_days` → 来自parts或dedup cleanup组件的Remove / TryRemove有 ≥ 50 % 返回`ZNONODE`（HC-3.22）；Keeper响应时间高，但Keeper CPU和内存**并未**耗尽；被插入表上TOO_MANY_PARTS（252），merge完成缓慢或完全不动；`asynchronous_insert_log`的flush由查询数限制触发。
**通常意味着：**每个replica按自己的计划裁剪过期去重记录，没有leader，因此各replica选择重叠的记录并各自发出单条删除：一个成功，其余得到`ZNONODE`——但每次尝试仍是一次带提交的复制Keeper写入。一张表几乎能产生某个服务的全部remove（现场：约95 %，两小时内约3亿次remove，峰值约150 000 removes/s，而通常上限约30 000 Keeper ops/s）。两个放大因素：分区键把每次插入拆成许多part（`(day, key % 10)` → 每次flush写10个part，每个都有自己的去重记录），以及服务器默认`insert_deduplication_version = 'compatible_double_hashes'`，它在过渡期间把每个哈希同时写入旧格式和新格式。
**验证：**每次插入的`part_log` NewPart行数——恒定的N（一次插入触及的分区数）就是放大倍数；`asynchronous_insert_log`的flush原因和`system.settings`中的`async_insert_max_query_number`（默认450）/ `async_insert_max_data_size`；Remove行的`errors`列（ZNONODE占比）。提高Keeper磁盘吞吐**没有帮助**——成本是提交，不是字节。
**修复/缓解：**现场清除它的做法：在插入用户profile中设`deduplicate_insert = 'disable'`（它会覆盖`insert_deduplicate`和`async_insert_deduplicate`；池化客户端连接只在重连时采用，因此效果逐渐显现）——仅限管道本身已保证exactly-once投递的场景。同时减少记录数量：更少、更大的异步flush（提高`async_insert_max_query_number`，现场用了约1000，以及`async_insert_max_data_size`），让一次插入落在一个分区的分区键，以及在所有服务器都运行可理解它的版本后，设`insert_deduplication_version = 'new_unified_hash'`（服务器设置）以停止双重写入。扩容运行merge的服务没有帮助。通过同样的文件中Remove计数和Keeper提交速率回到基线来验证。
**不要混淆：**P-63（znode在*累积*——这里在删除，只是过于频繁且冗余）；P-02（微小插入——喂养此问题的原因，可在`part_log`大小中看到）。
**公开参考：**文档 → 设置`deduplicate_insert`、`insert_deduplicate`、`async_insert_deduplicate`、`async_insert_max_query_number`、`async_insert_max_data_size`、`replicated_deduplication_window`；服务器设置`insert_deduplication_version`；`system.aggregated_zookeeper_log`。

### P-63磁盘元数据znode堆积——create远超remove，直到Keeper内存耗尽
**你看到：**`aggregated_zookeeper_log_paths_1_day` → 某个`/…/mergetree/_objects/N`类（或其他类）每天成功create超过成功remove数百万次，≥ 10×（HC-3.23——请求平衡，所以只是线索，不是证据）；`keeper/<member>.txt`的`mntr`中`zk_znode_count` / `zk_approximate_data_size`高且上升；随后Keeper返回ZOUTOFMEMORY，然后会话过期（打开会话时999 `Coordination error: Out of Memory`），表变为只读，part数攀升（P-57级联）。
**通常意味着：**保存在Keeper中的对象存储元数据（每个part的每个文件约一到三个znode）增长快于清理删除。现场出现part创建突发——向宽表插入1–6行的小插入、分区键把每次插入拆成128份、每个part数百个文件——把create从约350 000推高到每分钟710 000，而remove降到每分钟约50；不到一小时净堆积约3200万个znode，直到Keeper超过`keeper_server.max_memory_usage_soft_limit`并开始拒绝请求。延迟直到故障前不久仍接近正常。见过的第二个原因：混合版本滚动升级期间，固定在较旧`compatibility`的replica无法确认其他replica以`shared_merge_tree_use_outdated_parts_compact_format = 1`形式记录的过期part，因此`system.shared_merge_tree_outdated_parts`让每个replica都留在`replicas_to_confirm`中，什么也释放不了（没有错误，remove就是偏低）。
**验证：**`part_log` NewPart速率与每part行数，`system.parts`每part文件数 / 每表列数，每次插入的分区数；`keeper/<member>.txt`的`zk_znode_count`对比上一个bundle；混合版本情形，查看每个replica的`system.settings` `compatibility`和`system.shared_merge_tree_outdated_parts`。
**修复/缓解：**立即给Keeper内存余量（提高其内存和`max_memory_usage_soft_limit`）让它恢复服务，然后让积压merge下去。再降低创建速率：更大批次（客户端每几秒flush几MiB → 数十MiB和数十秒），更粗的分区，每part更少文件（更少列，小插入用Compact part），把`parts_to_delay_insert` / `parts_to_throw_insert`降到低位而不是数千。混合版本情形用`ALTER TABLE … MODIFY SETTING shared_merge_tree_use_outdated_parts_compact_format = 1`排空——一次性作用于所有replica，而不是滚动重启。Keeper侧`snapshot_distance` / `reserved_log_items`限制内存中保留的日志；如此大的状态还需要P-61的分块快照传输。
**不要混淆：**P-62（remove *过多*，不是过少）；P-61（已经很大的状态的快照发不出去——常是本模式的下一个阶段）。
**公开参考：**文档 → ClickHouse Keeper（`max_memory_usage_soft_limit`、`snapshot_distance`、`reserved_log_items`）、MergeTree设置`parts_to_delay_insert`、`parts_to_throw_insert`、`shared_merge_tree_use_outdated_parts_compact_format`；`system.aggregated_zookeeper_log`。

### P-64对象存储元数据读风暴——`_objects`上的MultiRead内出现数百万次Get
**你看到：**`aggregated_zookeeper_log_paths_1_day` → 某个`/…/mergetree/_objects/N/.dir`（或`_objects/N`）类占当天请求的大部分，几乎全是`in_multi`（HC-3.23）；`…_by_hour_2_days` → MultiRead携带的Get远多于独立请求；`…_by_minute_1_day` → 请求突发（HC-3.21）——子请求达到每秒数百万次，而独立流量停留在数万；异步插入超时（159），Keeper记录`Response queue is too big … Dropping responses`，Raft append耗时数秒。
**通常意味着：**加载一个part时从Keeper读取其文件列表，每个文件一次Get。文件数巨大的part会让每次加载part花费数百万次Get。现场发现的原因：旧的`compatibility`值让`input_format_json_infer_array_of_dynamic_from_array_of_different_types`关闭（自25.8起默认开启），于是混合类型的JSON数组被存为JSON元组——每个Wide part有0.3–130万个文件，每次加载part有100–370万次Get。超时的peer cache提示随后回退到完整读取，每个新pod都会重载每个Wide part。同样的风暴来自**重启**：多个replica同时启动会重读每个part（现场每个pod第一分钟约160 000次请求，`_objects` Get ×39，`blocks` Exists ×110于稳态），把Keeper延迟推到10–13秒并使会话过期。服务器回退也能造成同样结果：对比事故窗口与正常窗口，`_objects`路径只在受影响版本上高居榜首。
**验证：**`system.parts` / `system.columns`——热表的每part文件数（列 × 子列）；`system.settings`的`compatibility`和JSON推断设置；突发分钟内的`execution_log` uptime和`text_log_keeper_1_day`中的重启；版本变更前后的同样文件。
**修复/缓解：**在服务器profile中启用`input_format_json_infer_array_of_dynamic_from_array_of_different_types = 1`（或去掉旧的`compatibility`值），然后重写受影响列让现有part缩小——`ALTER TABLE … UPDATE col = col::String::JSON WHERE 1`或`ALTER TABLE … MODIFY COLUMN col JSON(max_dynamic_paths = 0)`。一次重启一两个replica，不要一起重启。若是回退，对比上一版本并降级或升级到修复该问题的构建。现场通过风暴停止、p99延迟回到约1秒来验证。
**不要混淆：**P-63（`_objects`在*增长*——create；这里是读取）；P-65（一个replica慢——这里每个replica都忙）。
**公开参考：**文档 → JSON数据类型（`max_dynamic_paths`）、设置`compatibility`、`input_format_json_infer_array_of_dynamic_from_array_of_different_types`；`system.aggregated_zookeeper_log`。

### P-65某个replica的Keeper请求慢，而同组其他replica正常
**你看到：**`aggregated_zookeeper_log_by_minute_1_day`（cloud：按`hostname`）→ 某个replica的加权延迟以秒计，而其他replica对同一集群保持在约7–15 ms，通常该replica的请求数骤降（HC-3.21）；`metric_log_by_host_3_days`中同一主机`zk_wait_us / zk_transactions`高（HC-3.17）；只有该replica上INSERT和merge失败。
**通常意味着：**问题在该replica到Keeper的路径上，而不是整个Keeper：其会话所在的Keeper成员（某案例中该成员磁盘的changelog `fsync`慢）、它的网络，或replica自身卡住（另一案例中一个超大的`SELECT *`填满了该pod的网络缓冲区；查询结束后恢复）。
**验证：**`system.zookeeper_connection`指明该replica会话所在的成员——与健康replica使用的成员对比；每个成员的`keeper/<member>.txt` `mntr` `zk_avg_latency` / `zk_max_latency` / `zk_outstanding_requests`；`query_log_details`中同一分钟该replica上的巨大查询。
**修复/缓解：**把该replica移出慢成员——重启它会重连到别处；然后修复成员的磁盘（Keeper的changelog需要自己的低延迟磁盘）。对卡住的replica，找到并kill查询，并为发出该查询的用户限制结果大小（`max_result_bytes`、`max_execution_time`）。
**不要混淆：**P-57（整个集群出问题——每个replica同时慢）；P-59（饱和的schedule/fetch池也会抬高某台主机的Keeper等待——先检查池）。
**公开参考：**文档 → `system.zookeeper_connection`、ClickHouse Keeper four-letter command（`mntr`）、Keeper硬件建议。

### P-66早期25.10构建中的Keeper客户端缺陷——Multi缓慢、`CreateIfNotExists`挂起、会话需要数分钟才恢复
**你看到：**`aggregated_zookeeper_log_by_hour_2_days` → Multi平均7–23秒，`CreateIfNotExists`平均数分钟（HC-3.22），错误集中在replica的`virtual_parts`路径；`text_log_keeper_1_day` → 丢失的会话需要4–15分钟才重连（其他版本约25秒内重连）；interserver连接增长到数千；`system.databases`中有Replicated数据库。
**通常意味着：**两个上游已修复的缺陷。Replicated数据库的集群刷新发出了过多由watch驱动的读取（ClickHouse/ClickHouse#92339使其更糟；#96897修复，在25.10.1.7464——25.10.1.7462没有该修复）。开启全局profiler时，每10秒一次的signal不断重置Keeper socket的发送超时，于是死亡会话数分钟内未被发现（#96601修复，在25.10.1.7545和25.12.1.1440）。
**验证：**`system.version`对比修复构建；`system.server_settings` / `system.settings`中的全局profiler（`global_profiler_real_time_period_ns`、`global_profiler_cpu_time_period_ns`）；Replicated数据库数量。
**修复/缓解：**升级到包含两个修复的构建（见上）；在此之前，禁用全局profiler（服务器设置`global_profiler_real_time_period_ns = 0`和`global_profiler_cpu_time_period_ns = 0`；两者默认10秒）以恢复快速重连。升级后，只剩下启动期间短暂的会话丢失。
**不要混淆：**P-65（一个replica的路径）；P-64（读风暴——计数高，不是单个操作慢）。
**公开参考：**GitHub ClickHouse/ClickHouse#92339、#96897、#96601；文档 → `system.aggregated_zookeeper_log`。

### P-67分区操作在大量小part上执行时Remove / Check / Create缓慢
**你看到：**`aggregated_zookeeper_log_by_hour_2_days` → 在`ATTACH`/`MOVE`/`REPLACE PARTITION`或大`INSERT … SELECT`的小时内，Remove平均数十秒，Check和Create数秒到数十秒（HC-3.22）；`part_log` → 一次插入向非常宽的表创建约一千个小part。
**通常意味着：**一次插入使用`min_insert_block_size_rows = 0`（或摄入工具的暂存模式）每个块生成一个part——数百到数千个part——随后分区操作通过Keeper同步每一个part（`shared_merge_tree_sync_parts_on_partition_operations = 1`，默认），在一个数百列的表上每个part都是一串Keeper写入。
**验证：**同一小时内插入和分区操作的`query_log_details`；每次插入的`part_log` NewPart数量和大小；表的`system.columns`列数。
**修复/缓解：**分区操作期间设`shared_merge_tree_sync_parts_on_partition_operations = 0`，并直接插入目标而非暂存（使用摄入工具的直接模式）；保持`min_insert_block_size_rows`为默认值，使一次插入形成大块。
**不要混淆：**P-66（客户端缺陷导致的慢操作——与特定操作无关）；P-01（插入超过merge导致part堆积）。
**公开参考：**文档 → 设置`shared_merge_tree_sync_parts_on_partition_operations`、`min_insert_block_size_rows`；`ALTER TABLE … PARTITION`操作。

### P-68后台任务在Keeper上循环——某个组件发出服务器的大部分请求
**你看到：**`aggregated_zookeeper_log_by_hour_2_days` → 某个组件（例如`ReplicatedMergeMutateTaskBase::executeStep`和`StorageReplicatedMergeTree::fetchPart`）占所有Keeper请求的 ≥ 60 %，且数值比平时高出数小时，出现在一个replica上（cloud中的`busiest_replica`）（HC-3.22）；`…_paths_1_day` → 同一replica的路径上重复List/Exists/Get；其错误中混有`ZCONNECTIONLOSS` / `ZSESSIONEXPIRED`。
**通常意味着：**某个复制任务反复重试同一步骤而无进展——在某个replica上停止fetch（`SYSTEM STOP FETCHES`）可复现：其merge/fetch任务持续轮询源replica的路径，Keeper请求约为同组的130倍。任何卡在重试循环中的任务（持续失败的fetch、等待缺失part的merge）都有相同形态。
**验证：**该replica上的`system.replication_queue`（该表条目的`num_tries`、`last_exception`、`postpone_reason`）；`system.replicas`的`queue_size` / `absolute_delay`；fetch或merge是否被停止（`query_log_details`中的`SYSTEM STOP …`）。
**修复/缓解：**消除任务无法推进的原因——如果被停止则`SYSTEM START FETCHES` / `START MERGES`，修复失败的fetch（P-41 / P-44），或处理损坏队列条目的原因；Keeper负载会随之下降。
**不要混淆：**P-62（同样是一个组件，但针对去重路径的remove）；P-59（池饱和——任务是忙，不是循环）。
**公开参考：**文档 → `system.replication_queue`、`SYSTEM STOP/START FETCHES`、`system.aggregated_zookeeper_log`。

---

## 新增模式

以症状优先、bundle可观测的方式编写：**你看到**（文件 → 列 → 条件）· **通常意味着** · **验证**（第二个独立来源）· **修复/缓解**（带默认值和版本说明的设置）· **不要混淆** · **公开参考**（仅文档/GitHub）。移除所有标识符：组织、服务、主机名、数据库/表/列名、用户、案例或工单编号、人员。把该行加入快速参考表，并在`health-checks.md`/`error-codes.md`中相关检查或错误码指向它时交叉链接。
