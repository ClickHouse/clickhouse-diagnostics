# 文件指南 — 每个bundle（诊断包）文件的用途、先读什么、健康时是什么样

collector（采集器）产出的每个文件一条条目。在查看`bundle-layout.md` §4的列说明表**之前**先读本条：它说明该文件回答的问题、最先要算的三件事、健康服务器上是什么样、危险信号（附检查/模式ID）、陷阱，以及要与哪些其他文件搭配阅读。标有 *（经验值）*的阈值来自现场经验，其余是工具的alert rule（告警规则）或ClickHouse默认值。

时间窗口：`*_7_days`文件覆盖最近7天（或`-from/-to`）；`system.text_log`覆盖24小时，最多2000行；其他每个文件都是采集时刻的快照。

---

## 时点快照

### system.version
**为什么要采集：**每个阈值、默认值和bug修复说明都依赖版本；没有它，bundle的其余部分就无法安全解读。
**它回答的问题：**是哪个确切的服务器构建产出了这个bundle？
**先读：**单个`version`值（`MAJOR.MINOR.PATCH.BUILD`）；从v0.9起还有`hostname`（gov模式下为哈希）、服务器`timezone`和`collected_at`——采集时服务器的`now()`，其他文件中的每个DateTime都相对于它（告警检查据此计算时间窗口）。
**健康基线：**某个LTS小版本（`YY.3`、`YY.8`）的较新补丁版，或当前稳定版。
**危险信号：**某小版本的`.1`/`.2`补丁（不含该小版本的任何bug修复）；小版本已发布超过约12个月（超出支持期）；`system.clusters`/日志banner中各节点版本不一致（P-31、P-54）。
**陷阱：**Cloud构建（形如`26.2.1.558`）可能包含OSS tag中没有的改动。
**搭配文件：**所有文件——每条“默认值是X”的论断都依赖它（`clickhouse-source.md`）。

### system.clusters
**为什么要采集：**分布式和复制架构往往在节点之间的缝隙里失败——一个过期的host或一个`errors_count > 0`的节点，在打开任何表之前就能解释`ON CLUSTER`超时和fetch失败。
**它回答的问题：**这个节点认为自己属于什么拓扑，每个对端是否可达？
**先读：**不同的`cluster`名；每个cluster的行数（`shard_num`、`replica_num`）；`errors_count`、`slowdowns_count`、`estimated_recovery_time`；`is_local`。
**健康基线：**每个cluster都具有预期的shard（分片）×replica（副本）数，`errors_count = 0`，每个cluster恰好一行`is_local = 1`。
**危险信号：**`errors_count > 0`（分布式查询最近到该主机失败——HC-3.6、P-44）；已不存在的主机（`ON CLUSTER`超时，P-21/P-42）；一个声称参与复制的服务器上只有单节点cluster。
**陷阱：**cloud模式下`default` cluster是服务的副本集；gov模式下`host_name`为哈希；`database_replica_name`仅在 ≥ 23.5上存在；**不采集`is_active`**（查询它会让服务器去询问每个Replicated数据库的副本——在有数千个此类副本的服务器上要花几分钟）：Replicated数据库的副本健康状况在`system.database_replicas`（≥ 25.9）里。
**搭配文件：**`configuration/`的`<remote_servers>`、`system.replicas`、`system.replication_queue`、`system.database_replicas`、`system.replicas_answered`（cloud）。

### system.tables
**为什么要采集：**几乎每个part（数据分片）/merge（合并）/插入相关的finding（发现）都由schema解释——分区键、排序键、引擎、挂在源表上的materialized view（物化视图，MV）以及每表的`SETTINGS`覆盖。
**它回答的问题：**schema长什么样——引擎、键、分区、MV、每表设置？
**先读：**非system表按`engine`分组；在part/查询finding中出现的表的`partition_key`和`sorting_key`；每个源表的`MaterializedView`数量（`dependencies_table`）；`engine_full`中的`SETTINGS`覆盖（`parts_to_throw_insert`、`ttl_only_drop_parts`、`min_age_to_force_merge_seconds`、`storage_policy`）；用`total_rows` / `total_bytes`区分有数据的表和空表；在 ≥ 26.6上还有`target_table`，表明每个MV写入哪里（包括隐式的`.inner_id.*`目标）。要看整条管道的形态，请从dashboard（仪表盘）的Schema标签页打开schema graph（模式关系图）文件`schema_graph.html`，而不是去读数组。
**健康基线：**MergeTree系列表具有以低基数过滤列开头的排序键，分区键粒度为天/月，每个源表 ≤ 约10个MV，日志类表声明了TTL。
**危险信号：**大表上`sorting_key = ''`/`tuple()`、高基数分区键（P-03、P-50）；一个源表上挂了几十个MV（P-34）；DDL中有`TTL`但`part_log`中没有TTL活动（P-18）；`Nullable`键列；卡死中涉及`Join`/`Buffer`/`Kafka`引擎（P-21）。
**陷阱：**gov模式下`database`/`name`为哈希——`is_system_db`（v0.9）说明哪些行是system表；gov模式下不保留DDL/键（标识符和依赖数组都被哈希）；`create_table_query`携带注释和默认值——绝不可在机器外原样粘贴（`privacy.md`）；引擎凭据读作`'[HIDDEN]'`（≥ 23.x由服务器掩码，否则由collector脱敏）——来自此脱敏组件之前的工具构建且服务器为22.x的bundle仍可能带有原始凭据。
**搭配文件：**`system.parts`（解释数量）、`system.columns`（类型）、`query_log_details`（哪些表是热点）。

### system.view_refreshes (≥ 23.12)
**为什么要采集：**可刷新的materialized view（`REFRESH EVERY … / AFTER …`）按计划运行，而不是在插入时运行，因此`query_views_log`从来看不到它，`system.tables`也无法把它与普通MV区分开。这是唯一能说明它是否仍在刷新的表。
**它回答的问题：**可刷新视图是否按时刷新，最近一次失败说了什么？
**先读：**每个视图的`status`；`last_success_time`与采集时间以及`create_table_query`中视图的`REFRESH`间隔的对比；`exception`。
**健康基线：**每行都是`Scheduled`（或`Running`），`last_success_time`在采集时间的一个间隔之内，`exception`为空。
**危险信号：**`Disabled`（有人执行了`SYSTEM STOP VIEW`，或服务器在反复失败后将其禁用）；`last_success_time`早于数个间隔；任何`exception`——文本会指出失败的对象，通常是视图`SELECT`连接的一张已被删除或改名的表（HC-7.6）。
**陷阱：**低于23.12时不存在（不是finding）；cloud模式下每个副本一行——刷新只在一个副本上运行，其他副本报告`RunningOnAnotherReplica`；gov模式下用`has_exception`代替异常文本。
**搭配文件：**`system.tables`（视图的DDL及其`REFRESH`子句）、`schema_graph.html`（这些视图是粉红色节点）、`last_refresh_time`前后的`text_log`。

### system.data_skipping_indices
**为什么要采集：**对*有*跳数索引的列做慢过滤，与对没有跳数索引的列做慢过滤是两种不同的finding；DDL能说明是哪种，但只有读完每个`CREATE`才能知道。
**它回答的问题：**哪些表带数据跳数索引，建立在哪些表达式上，类型和粒度是什么？
**先读：**慢查询finding中出现的表上的索引；`type`与谓词形态是否匹配（范围用`minmax`，等值和子串用`bloom_filter`/`tokenbf_v1`/`ngrambf_v1`）；`granularity`。
**健康基线：**热点表上有少量索引，且建立在WHERE子句实际使用的列上。
**危险信号：**索引建在没有任何查询过滤的列上（只有成本没有收益）；高基数且只用于范围谓词的列上建`bloom_filter`；`query_analysis`显示有匹配索引的表上`SelectedMarks` ≈ 总marks（P-53——索引存在但没有起到裁剪作用）。
**陷阱：**只有定义——没有每副本的大小列，因此无法说明索引有多大；gov模式下丢弃`expr`。
**搭配文件：**`system.tables`（决定裁剪成败的`ORDER BY`）、`query_analysis/profile_events_compare`、`schema_graph.html`（丝带显示每个节点的索引数量）。

### system.columns
**为什么要采集：**类型决定merge内存、压缩和MV兼容性；错误消息中提到的失败列要在这里查。
**它回答的问题：**使用了哪些类型，表有多宽？
**先读：**每表列数（`count()`）；值得标记的类型：键中的`Nullable(...)`、本可用`LowCardinality`/Enum却用了`String`、`JSON`/`Object`/`Dynamic`/`Variant`、被当作兜底的`Map(String, String)`、MV与目标表之间不一致的`FixedString`。
**健康基线：**紧凑的类型化schema；只有NULL有意义时才用Nullable。
**危险信号：**merge发生OOM或崩溃的表上的JSON/Object列（P-11、P-30）；有界取值集合用了`String`列（P-50）；MV的`as_select`输出与目标表之间的类型差异（P-32）。
**陷阱：**文件很大（每列一行）；gov模式下省略`default_expression`；这里没有每列的大小统计（只在`system.parts_columns`中有，但未采集）。
**搭配文件：**`system.tables`，以及错误消息中出现列名时的`part_log`/`crash_log`。

### system.parts
**为什么要采集：**每个分区的part（数据分片）数量是MergeTree健康状况的最佳单一指标——它能在变成错误之前预测`TOO_MANY_PARTS`、慢读取和mutation（变更）积压。
**它回答的问题：**MergeTree健康吗——有多少part、多大、多碎、分布在哪些磁盘上？
**先读：**过滤`active = 1`；按`(database, table, partition_id)`统计part；`countIf(level = 0)`；每个part的`avg(rows)`；每个数据库的`sum(bytes_on_disk)`；`data_uncompressed_bytes / data_compressed_bytes`。
**健康基线：**每个分区几十个part，level-0 part只占少数，繁忙表上平均每part ≥ 100k行，压缩比 > 3，`system`数据库很小。
**危险信号：**单个分区 > 300个part（HC-2.1、P-01），数百个level-0 part（HC-2.2），平均行数 < 10k（HC-2.3、P-02），单个part > 150 GiB（HC-2.9），`system`属于最大的数据库（HC-4.4、P-17），有TTL的表上`delete_ttl_info_*` = 1970（P-18），残留的`is_frozen = 1`，`disk_name`意外指向冷卷（P-45）。
**陷阱：**当前bundle**只包含active part，按从大到小排序，上限50 000行**——恰好50 000行意味着最小的part被截掉了（由小part组成的表，其每分区计数可能被低估）；2026年9月之前的bundle还包含inactive part——无论如何都要过滤`active = 1`；gov模式下`partition`（值）被哈希，而`partition_id`没有；即使在cloud模式下也只是单个副本。
**搭配文件：**`system.tables`（键解释计数）、`part_log`（merge是否在发生）、`system.merges`（是否卡住）、`system.mutations`（是什么把它们钉住）。

### system.parts_by_partition (v0.9, essential)
**为什么要采集：**不读取每个part就能得到每个分区的part数量——`TOO_MANY_PARTS`所依赖的那个数字，即使在跳过`system.parts`的essential采集中也会保留。
**它回答的问题：**哪些分区的part在堆积？
**先读：**按`(database, table, partition_id)`的`parts`；`min_level`（0 = 尚未合并的插入仍然存在）；`rows / parts`。
**健康基线：**空文件——它只保留active part超过100个的分区。
**危险信号：**`parts` > 300（`too_many_parts`告警，HC-2.1、P-01）；`parts`接近`parts_to_delay_insert`（1000）或`parts_to_throw_insert`（3000）= 插入已被延迟或即将以252失败。
**陷阱：**最多2000个分区，按从多到少排序；只含用户数据库；gov模式下`database`/`table`为哈希；cloud模式下单副本。
**搭配文件：**`system.parts`（若已采集）、`part_log_3_days`（插入速率对比merge）、`system.merges`。

### system.settings和system.server_settings（server_settings在onprem/gov上 ≥ 23.3）
**为什么要采集：**“哪些设置偏离了默认值”过去需要`configuration/`，而cloud bundle从来不带它，gov也从不提供；这两张表直接回答该问题，并把ClickHouse默认值放在值旁边。
**它回答的问题：**哪些设置被调整得偏离了默认值——查询/profile设置（`system.settings`）和服务器级设置（`system.server_settings`）？
**先读：**两个文件中的`WHERE changed = 1`；内存（`max_memory_usage`、`max_server_memory_usage*`、`max_bytes_before_external_group_by`）、池（`background_pool_size`、`background_fetches_pool_size`、`background_merges_mutations_concurrency_ratio`、`number_of_free_entries_in_pool_to_execute_mutation`）、并发（`max_concurrent_queries*`）、缓存（`mark_cache_size`、`uncompressed_cache_size`）、插入（`async_insert*`、`parallel_distributed_insert_select`）、`compatibility`；`keep_free_space_bytes`、`max_table_size_to_drop`。
**健康基线：**一份很短的`changed = 1`列表，且与负责人知道自己设置的内容一致；服务器内存比例 ≤ 0.9；池大小与CPU数匹配。
**危险信号：**成对设置只改了一侧（P-16）；`max_server_memory_usage_to_ram_ratio` > 0.9或每查询`max_memory_usage`高于服务器上限（P-10）；升级后`compatibility`仍设为旧版本（P-54）；`parts_to_throw_insert`之类的保险设置从未改回（P-01）；`wait_for_async_insert = 0`（P-33）；`max_concurrent_queries`被调低（P-20）；只存在于某些版本的`merge_tree`覆盖。
**陷阱：**`system.settings`反映的是**collector所用用户/profile**的设置，而不是每个用户——每用户profile在`configuration/users.d`；MergeTree表设置**不在**这里——服务器级层在`system.merge_tree_settings`（v0.7，见下），每表覆盖在`system.tables.engine_full`；gov模式下可识别身份的`server_settings`值读作`REMOVED`，但名称和`changed`标志会保留；< 23.4上没有`default`列，也没有`server_settings`文件——请在对应tag的源码中核实设置默认值（`clickhouse-source.md`）。
**搭配文件：**`configuration/`（交叉核对）、`metric_log`（池是否真的饱和）、`host_info`（cgroup限制对比`max_server_memory_usage`）。

### system.merge_tree_settings (v0.7)
**为什么要采集：**第三层设置——每个MergeTree表从服务器`<merge_tree>`块继承的内容，除非它自己的`SETTINGS`子句覆盖。`TOO_MANY_PARTS`阈值就在这里，而在SharedMergeTree上，还有两个决定拥有数万张表的集群如何加载后台池的旋钮：`shared_merge_tree_parts_load_batch_size`（单表每轮调度抓取的part数，默认32）和`shared_merge_tree_leader_update_period_seconds`（每表每周期一次leader选举，默认30 s）。只有知道当前值，修改它们的建议才是安全的。
**它回答的问题：**服务器级MergeTree默认值是什么，哪些被调整过？
**先读：**`WHERE changed = 1`；`parts_to_throw_insert` / `parts_to_delay_insert` / `max_parts_in_total`；SMT上则是上述两个`shared_merge_tree_*`设置，并结合`system.tables`计算选举/s ≈ Shared*表数/周期；`max_bytes_to_merge_at_max_space_in_pool`；`min_bytes_for_wide_part`；`storage_policy`。
**健康基线：**一份负责人能认出的简短`changed = 1`列表；在拥有数万张表的SMT上，批大小和leader周期高于默认值（在该规模下64 / 300已得到验证）。
**危险信号：**`parts_to_throw_insert`作为保险被调高后从未改回（P-01）；有 ≥ 5 000张Shared*表且某个池被钉住时仍用SMT默认值（HC-2.16、P-59）；没有任何表使用的`storage_policy`（`system.tables.storage_policy`）。
**陷阱：**仅限服务器级——表自己的`SETTINGS`（`system.tables.engine_full`）优先；没有`default`列（22.8下限），因此默认值必须从对应版本的文档/源码获取；cloud模式下从一个副本读取（`<merge_tree>`块在整个集群部署）；gov模式下`storage_policy` / `disk`用盐哈希，路径、工作负载和列清单值读作`REMOVED`。
**搭配文件：**`system.tables`（`engine`、`engine_full`、`total_rows`）、`metric_log_by_host_3_days`（每小时选举次数、池）、`system.server_settings`（池大小）。

### system.replicas_problem_tables
**为什么要采集：**有五个`system.replicas`列——`log_max_index`、`log_pointer`、`total_replicas`、`active_replicas`、`zookeeper_exception`——需要从Keeper读取，请求其中任何一个都会让服务器从Keeper读取**每张表**的状态：每张复制表一次往返，在拥有数万张表的服务器上要几分钟。本文件最多只为200张表请求这些列，这些表由廉价列挑选：先选所有只读、会话过期、排队、延迟或正在做part检查的表，再抽样一些健康的表。
**它回答的问题：**对于陷入麻烦的表，是否所有副本都还活着，本副本是否落后于日志，Keeper拒绝了什么？
**先读：**`active_replicas`对比`total_replicas`；`log_max_index − log_pointer`（本副本尚未复制进队列的条目）；只读行上的`zookeeper_exception`。
**健康基线：**`active_replicas = total_replicas`，`log_pointer`等于或比`log_max_index`大一，`zookeeper_exception`为空。
**危险信号：**`active_replicas < total_replicas`（HC-3.3）；`total_replicas`高于实际存在的服务器数（P-43——保留健康样本正是为了让幽灵副本即使在看起来正常的表上也暴露出来）；日志差距很大；只读表上有`zookeeper_exception`（HC-1.4、P-40）。
**陷阱：**最多200张表——这里没有的表可能只是没被选中（它仍在`system.replicas`中，带廉价列）；cloud：按副本（`hostname`），每个副本各自挑选自己的200张；gov：名称和异常文本都被哈希。
**搭配文件：**`system.replicas`（廉价列，覆盖每张表）、`system.replication_queue`、`system.zookeeper_connection`。

### system.database_replicas (≥ 25.9)
**为什么要采集：**它是`system.clusters.is_active`的廉价替代：本服务器上每个Replicated数据库副本是否可写、是否持有Keeper会话，而无需询问每个Replicated数据库的每个副本。
**它回答的问题：**本服务器上是否有Replicated数据库处于只读？
**先读：**`is_readonly`、`is_session_expired`（行按只读优先排序）；原因在`text_log` / `errors`（999、242）和`distributed_ddl_queue`中。
**健康基线：**到处`is_readonly = 0`、`is_session_expired = 0`。
**危险信号：**某个数据库只读或会话过期——其DDL（`ON CLUSTER`、通过Replicated数据库执行`CREATE`/`ALTER`）会卡住（HC-3.11、`distributed_ddl_queue`）。
**陷阱：**低于25.9时不存在——不是证据；`max_log_ptr` / `log_ptr` / `total_replicas` / `zookeeper_exception`是刻意不采集的（每个都会从Keeper读取所有数据库的状态）——DDL延迟在`distributed_ddl_queue`；cloud：按副本（`hostname`）。
**搭配文件：**`system.distributed_ddl_queue`、`system.databases`（Replicated数量）、`system.zookeeper_connection`。

### system.replicas_answered (cloud)
**为什么要采集：**每个请求都以`skip_unavailable_shards = 1`运行，因此一个副本宕机不再让每个`clusterAllReplicas` collector失败——它只是从这些结果中消失。本文件把它点名。
**它回答的问题：**服务的每个副本都应答了吗？
**先读：**`answered = 0`的行（排在最前）。
**健康基线：**每一行都是`answered = 1`。
**危险信号：**任何`answered = 0`（预处理中的HC-0）——该副本的行在part_log、query_log、metric_log、replicas以及所有其他每副本文件中都不存在，因此“没看到问题”对它没有任何意义；它往往正是出问题的那个副本。
**陷阱：**名称方面没有陷阱：应答按位置与配置的主机匹配（`clusterAllReplicas`按集群顺序把每个副本编号为各自的分片，`_shard_num`就是该编号），因此别名、IP端点和共享短名的副本都无法误导它。
**搭配文件：**`system.clusters`（`default` cluster）、`execution_log.txt`。

### system.detached_parts
**为什么要采集：**detached part（分离part）是服务器自己记录的、它无法信任或无法重新挂载的数据；它们的`reason`区分损坏与复制簿记问题，而且它们仍占用磁盘。
**它回答的问题：**是否有东西被作为损坏、意外或手动分离的数据搁置——它在浪费磁盘吗？
**先读：**按`database, table, reason`的`count()`和`sum(bytes_on_disk)`；`modification_time`（≥ 23.11）用于确定事件日期。
**健康基线：**空文件。
**危险信号：**`reason IN ('broken', 'broken-on-start', 'covered-by-broken')` → 损坏或版本偏差（HC-2.11、P-31）；复制事故后出现大量`unexpected`/`ignored`/`clone`（P-41/P-43）；磁盘写满时分离了数GiB数据（HC-4.1）。
**陷阱：**< 22.11的root变体没有`bytes_on_disk`；gov丢弃`disk`/`path`；`reason = ''` = 手动`DETACH`；不列出`system`、`information_schema`和`INFORMATION_SCHEMA`数据库（服务器会列出每张表的`detached/`目录——在元数据存放在Keeper的对象存储磁盘上，每张表一次远程调用——并在列举前跳过被过滤的数据库）；会列出materialized view目标表（`.inner*`）——损坏的part确实会落在那里；在SharedMergeTree上，broken-on-start part首先出现在日志和错误中（`TOO_MANY_UNEXPECTED_DATA_PARTS`，可疑损坏part上限），因此即使本文件为空也要读`text_log` / `errors`。
**搭配文件：**`system.errors` 40/226/246、`modification_time`前后的`logs/`、`system.disks`。

### system.disks
**为什么要采集：**磁盘写满会把其他所有症状（merge失败、只读副本、插入错误243）归结为一个根因；这项检查成本低且具有决定性。
**它回答的问题：**ClickHouse可写入的每个卷还剩多少空间？
**先读：**`name`、`type`、`free_pct`；`unreserved_space`对比`free_space`（运行中merge的预留）。
**健康基线：**数据卷剩余 > 30% *（经验值）*，热数据为`type = 'local'`，除非有意使用S3 tier（分层）。
**危险信号：**承载用户数据的磁盘`free_pct < 15`（critical，HC-4.1），`< 5`（插入/merge以243失败）。哪些磁盘承载数据，来自`system.tables.storage_policy`中策略对应的`system.storage_policies.disks`——没有任何用户表策略点名的磁盘不算数据容量（在托管服务上：那是文件系统缓存，被设计为填满），在那里读到低值是信息而不是critical；出现499错误时的对象存储磁盘（P-45）；冷磁盘收到新数据（P-45 `move_factor`）。
**陷阱：**`free_space`/`total_space`是人类可读字符串——用`free_pct`或自行解析；cloud模式下每个副本一行；`unreserved_space`仅在 ≥ 22.10上存在。
**搭配文件：**`host_info.disks`（挂载级视角）、`system.parts`（是什么在填满它）、`configuration/`的`<storage_configuration>`。

### system.errors
**为什么要采集：**最快的分诊信号——每个错误码一行，带计数和最近发生时间，因此在读任何日志之前就能知道主要失败类别。
**它回答的问题：**本服务器自启动以来产生过哪些错误码，频率如何，最近一次是什么时候？
**先读：**按`value`排的前10；`last_error_time`相对于采集时间；前几大错误码的`last_error_message`（用`error-codes.md`映射错误码）。
**健康基线：**客户端错误码（47、60、62）计数低，内存/Keeper/part系列近期没有发生。
**危险信号：**241（P-10）、252（P-01）、999/242（P-40）、243（HC-4.1）、173/76（P-13）、49/1001（P-30）、40/226（P-31）、499（P-45）、202（P-20）——尤其是`last_error_time`落在事故窗口内时。
**陷阱：****自服务器启动起累计**——90天uptime内10 000次命中不等于3小时内10 000次；要结合uptime看（dashboard的`uptime`，或服务器随主机重启时的`host_info.os.uptime_seconds`）。gov模式下消息被哈希、trace为空。Cloud：每个副本一行（`hostname`，v0.9），各取最大的100个——按错误码求和得到集群值。
**搭配文件：**`query_log_details.exception_code`（哪些查询）、`part_log.error`（哪些merge）、`text_log`/`logs/`（上下文）。

### system.merges
**为什么要采集：**一个运行数小时或接近内存上限的merge，是池看起来空闲而part积压却增长的常见原因；只有实时快照能展示它。
**它回答的问题：**现在正在合并什么，是否有merge卡住或过大？
**先读：**`elapsed`、`progress`、`num_parts`、`total_size_bytes_compressed`、`memory_usage`、`is_mutation`、`merge_algorithm`。
**健康基线：**少量merge，每个远不到一小时且`progress`在推进；空闲服务器上为空是正常的。
**危险信号：**`elapsed`以小时计且`progress` < 0.5（HC-2.5、P-04）；`memory_usage`接近上限（P-11）；`is_mutation = 1`的merge占用槽位（P-05/P-14）；同一个`result_part_name`在`part_log`中反复失败。
**陷阱：****空文件只意味着那一刻没有merge在运行**，而不是merge坏了；快照无法显示吞吐量——用`part_log`。
**搭配文件：**`part_log`（历史）、`system.mutations`（`parts_to_do_names` ∩ merge源）、`metric_log`（池饱和）。

### system.mutations
**为什么要采集：**待处理的ALTER/DELETE/UPDATE工作会阻塞DDL、钉住part并消耗merge能力；只有这里能看到积压及其年龄。
**它回答的问题：**哪些ALTER/DELETE/UPDATE操作仍在待处理，持续了多久，它们被阻塞了吗？
**先读：**每表计数；年龄 = 采集时间 − `create_time`；`parts_to_do`；`command`（重型`MODIFY COLUMN`？行级`DELETE`/`UPDATE _row_exists`？）；`parts_to_do_names`对比`system.merges`。
**健康基线：**为空，或只有少量近期mutation且`parts_to_do`在减少。
**危险信号：**超过3小时且`parts_to_do > 0`（HC-8.1、P-14）；单表 > 100个（HC-8.2、P-15/P-05）；`is_killed = 1`仍然存在（cloud；一段时间内正常，P-14）；对含NULL的数据做类型转换的命令（P-15）。
**陷阱：**完成的mutation会被服务器清理，因此文件中只有积压；**不采集**`latest_fail_reason`——失败的mutation表现为“旧且parts_to_do未降”再加上`part_log`的`MutatePart`错误；`is_killed`只存在于cloud变体；gov模式下`command`被哈希。
**搭配文件：**`part_log`（`event_type = 'MutatePart' AND error != 0`）、`system.merges`、`system.parts`（被点名的part是否巨大？）。

### system.replicas
**为什么要采集：**负载下的复制集群会逐个副本退化：只读状态、过期的Keeper会话和`absolute_delay`显示哪个副本在落后以及写入是否仍被接受。
**它回答的问题：**每张`Replicated*`表上的复制是否健康——是否连接Keeper、可写、已追上？
**先读：**`is_readonly`、`is_session_expired`、`absolute_delay`、拆分为`inserts_in_queue`/`merges_in_queue`/`part_mutations_in_queue`的`queue_size`、`parts_to_check`、`queue_oldest_time`，然后是`last_queue_update_exception`；对于Keeper支持的列（`active_replicas`/`total_replicas`、`log_max_index`/`log_pointer`、`zookeeper_exception`——只读副本背后的原因）请打开`system.replicas_problem_tables`。v0.7起的cloud bundle增加了`hostname`（每表**每副本**一行）、`oldest_part_to_get`和`inserts_oldest_time`——先按`hostname`分组，再看每个主机上哪些表在`inserts_in_queue`中领先。
**健康基线：**`is_readonly = 0`，`absolute_delay`只有几秒，`queue_size`小且在下降；在`replicas_problem_tables`中，`active_replicas = total_replicas`。
**危险信号：**`is_readonly = 1`（critical，HC-1.4、P-40），`absolute_delay > 60`（HC-3.1——ReplicatedMergeTree上为P-41；SharedMergeTree上这是该副本尚未抓取的part，P-59，多个主机出现相同的`oldest_part_to_get`属于同一个finding），`parts_to_check > 0`（可疑part），`queue_oldest_time`是数小时前，`last_queue_update_exception`非空（副本无法刷新自己的队列）；在`replicas_problem_tables`中，`active_replicas < total_replicas`（HC-3.3——死副本或Keeper中的幽灵，P-43）以及非空的`zookeeper_exception`（Keeper侧故障——与`system.errors` 999/242搭配）。
**陷阱：**空文件 = 没有复制表（单节点上正常）；每表每副本一行；onprem/gov文件按`absolute_delay DESC`排序，因此第一行最差；cloud文件按只读 → 过期 → 延迟 → `inserts_in_queue`排序，并且**每个主机最多200行**——恰好200行的主机被截断，其健康表根本没有列出（请在`system.tables`中统计表数）；SharedMergeTree上，当别处的merge覆盖了缺失的part时`inserts_in_queue`会下降，因此数字下降并不证明已追上（0才是证明）；SMT上`log_max_index`/`log_pointer`为0、`queue_oldest_time`为1970——这不是错误；gov模式下`zookeeper_exception`和`last_queue_update_exception`被哈希（空值仍为空，因此“这个副本到底有没有报错？”仍然可见）。
**搭配文件：**`system.replicas_problem_tables`（Keeper支持的列）、`system.replication_queue`（原因）、`metric_log.zk_*`（Keeper健康）、`system.errors` 999/242。

### system.replication_queue
**为什么要采集：**当副本落后时，队列说明*为什么*——受fetch限制（`GET_PART`）还是受merge限制（`MERGE_PARTS`），推迟原因和最后的异常；任何类型超过约60个条目都意味着某个副本正在变得不可用 *（经验值）*。
**它回答的问题：**副本在等着做什么，为什么没做？
**先读：**按`type`的`count()`（GET_PART = 受fetch限制，MERGE_PARTS = 受merge限制，MUTATE_PART，ALTER_METADATA）；`postpone_reason`的取值；`last_exception`非空的行；这些行的`num_tries`；最早的`create_time`；`is_currently_executing`计数。
**健康基线：**为空，或只有少量`create_time`较近且`last_exception`为空的条目。
**危险信号：**任何`type`超过60个条目 *（经验值）*（HC-3.4），`last_exception`含232/234（源端part缺失 → P-41/P-43）、210/209（fetch网络故障 → P-44）、40/226（源损坏 → P-31）；`postpone_reason`为 “N fetches already executing, max N”（fetch池太小，P-41）、“because part … is not ready” 链；仍排队但`num_tries`很高的条目（已重试并失败，而不只是等着轮到它）。
**陷阱：**gov模式下`database`、`table`、`replica_name`、`postpone_reason`和`last_exception`都被哈希（空值仍为空，因此“有没有异常”仍然可见）；`num_tries`不被哈希。
**搭配文件：**`system.replicas`、`configuration/`（`background_fetches_pool_size`、`<interserver_http_host>`）、`system.clusters.errors_count`。

### system.processes
**为什么要采集：**采集时刻正在运行什么——卡住的`BACKUP`、`OPTIMIZE FINAL`或失控查询在聚合日志中原本不可见。
**它回答的问题：**采集那一刻正在运行什么？
**先读：**`elapsed`、`memory_usage`、`read_rows`/`total_rows_approx`（进度）、`user`、`is_cancelled`。
**健康基线：**collector自己的查询加上少量短查询。
**危险信号：**`elapsed`以小时计的查询（卡住的`BACKUP`、`OPTIMIZE FINAL`、`ALTER`、失控的`SELECT`），从未结束的`is_cancelled = 1`行，同一用户的大量行（P-20/P-55）。
**陷阱：**单次快照——不存在并不能证明事故没发生；`query_id`让你可以交叉引用`query_analysis/`（若已采集）。
**搭配文件：**`query_log_details`（相同hash的历史）、`system.merges`。

### system.dictionaries
**为什么要采集：**失败或过大的字典会静默破坏每个调用`dictGet`的查询，并可能占用大量RAM。
**它回答的问题：**外部字典是否已加载、是否新鲜、大小是否合理？
**先读：**`status`、`last_exception`、`bytes_allocated`（总和对比RAM）、`element_count`、`hit_rate`/`found_rate`、`loading_duration`、`last_successful_update_time`对比`lifetime_max`。
**健康基线：**全部`LOADED`，`last_exception = ''`，`bytes_allocated`总量只占RAM一小部分，更新在`lifetime_max`内。
**危险信号：**`FAILED`/`FAILED_AND_RELOADING`（HC-9.1——源不可达、凭据、schema），采集时`LOADING`（HC-9.2），`bytes_allocated`达数十GiB（HC-9.3、P-12），`cache`布局字典`hit_rate`低，`last_successful_update_time`过旧。
**陷阱：**gov丢弃`source`/`last_exception`/`comment`；cloud中为每副本状态（每副本一行）。
**搭配文件：**`query_log_details` 156/36错误、`configuration/`字典定义。

### system.crash_log
**为什么要采集：**崩溃是服务器能记录的最高严重级别事件；定位修复需要trace和触发的`query_id`。
**它回答的问题：**服务器是否因致命信号死亡，何时，当时在做什么？
**先读：**`event_time`、`signal`（11 SIGSEGV、6 SIGABRT、7 SIGBUS）、`version`、`query_id`、`trace_full`的前几帧。
**健康基线：**0字节文件（文件存在，内容为空）。
**危险信号：**任何一行 = critical（HC-1.1、P-30）；多行顶部帧相同 = 确定性触发；`query_id`有值 = 某个查询导致（在`query_log_details`中找它的hash）。
**陷阱：**gov从v0.9起采集它，但不含`trace`/`trace_full`，且`query_id`被哈希；被OS OOM kill的情况在这里**没有**行（改为在`logs/`中找重启，P-13）；cloud上该表只存在于崩溃过的副本，因此只要有任何副本从未崩溃，扇出就会失败——`system.crash_log_presence`（cloud，v0.9）列出拥有该表的副本：为空 = 没人崩溃；有行但没有`crash_log`文件 = 崩溃了但读不出来。
**搭配文件：**`logs/`的`<Fatal>`块（完整符号化堆栈）、`system.version`、`query_log_details`。

### system.stack_trace
**为什么要采集：**对于正在发生的卡死，线程堆栈是看清所有人在等哪个锁或外部调用的唯一办法。
**它回答的问题：**采集时每个线程在做什么——只对此刻正在发生的卡死有用。
**先读：**线程数；按`thread_name`分组；寻找大量线程处于同一个锁/等待帧。
**健康基线：**几百个线程，大多空闲在池等待中。
**危险信号：**数百个线程处于同一个mutex/条件变量帧（死锁或锁护送，P-22）；线程卡在Keeper客户端调用（P-40）或S3 I/O（P-45）。
**陷阱：**gov中不采集；文件大、符号多，能识别构建；只有bundle是在卡死**期间**采集的才有意义。
**搭配文件：**`system.processes`、`system.merges`。

### system.metrics, system.events, system.asynchronous_metrics
**为什么要采集：**小时聚合无法说明*现在*什么是真的，而`system.errors`没有分母；这三者是实时仪表、累计计数器和服务器`Uptime`。
**它回答的问题：**本服务器此刻是否持有Keeper会话；副本是否只读；有多少fetch/merge在运行；自启动以来见过多少对象存储和Keeper错误，按uptime小时计？
**先读：**`metrics`：`ZooKeeperSession`、`ZooKeeperWatch`、`ReadonlyReplica`、`ReplicatedFetch`、`Background*PoolTask`、`S3Requests`、`FilesystemCacheSize`、`MetadataFromKeeperCacheObjects`（cloud：按`hostname`）；`asynchronous_metrics`：`Uptime`、`ReplicasMaxAbsoluteDelay`、`ReplicasSumQueueSize`、`MaxPartCountForPartition`、`NumberOfTables`、`NumberOfDatabases`；`events`：`ZooKeeperHardwareExceptions`、`ZooKeeperUserExceptions`、`S3ReadRequestsErrors`、`ReadBufferFromS3RequestsErrors`、`ReplicatedPartFailedFetches`、`RejectedInserts`、`DelayedInserts`、`FailedQuery`。
**健康基线：**`ZooKeeperSession ≥ 1`，`ReadonlyReplica = 0`，异常和错误计数器相对`Uptime`极小，各副本的`MetadataFromKeeperCacheObjects`相近。
**危险信号：**`ZooKeeperSession = 0`（HC-3.12）；`ReadonlyReplica > 0`（HC-1.4）；某副本的`MetadataFromKeeperCacheObjects`约为1而对端有10⁵（P-58）；`ZooKeeperHardwareExceptions`达数百万（P-57）；`S3*RequestsErrors`在增长（HC-4.10）；`Uptime`只有几分钟 = 服务器重启过（HC-1.2）。
**陷阱：**`events`和`system.errors`自启动起累计——除以`Uptime`，绝不要当作速率；`asynchronous_metrics`每分钟刷新，因此`Uptime`最多滞后60 s；每核行被刻意丢弃；gov丢弃按对象计的仪表。
**搭配文件：**`system.errors`（相同计数器，带消息）、`metric_log_coordination_3_days`（相同计数器随时间的变化）、`zookeeper_connection`。

### system.databases和system.storage_policies
**为什么要采集：**`system.tables`无法区分`Atomic`和`Replicated`数据库，它给出`storage_policy`名字却不说明背后是什么。
**它回答的问题：**有多少个`Replicated`数据库（Keeper负载乘数）；用户表是否位于对象存储上？
**先读：**`databases`：按`engine`计数；`storage_policies`：`system.tables.storage_policy`所用策略的`disks`，与`system.disks.type`连接。
**健康基线：**少量`Replicated`数据库；能说出策略对应的磁盘名。
**危险信号：**当Keeper是finding时，数百个`Replicated`数据库 × 数十个副本（HC-3.13、P-57）；出现107/`NoSuchKey`错误时，用户表背后是`ObjectStorage`磁盘（HC-4.0、P-58）。
**陷阱：**这里的`Replicated`是*数据库*引擎，与`ReplicatedMergeTree`表无关；gov用与`system.tables`/`system.disks`相同的盐哈希名称，因此连接仍然有效。
**搭配文件：**`system.tables`（`storage_policy`、引擎）、`system.disks`、`configuration/`的`<storage_configuration>`。

### system.zookeeper_connection (≥ 23.8)
**为什么要采集：**这是唯一说明本服务器与哪个Keeper节点通信以及**会话有多老**的地方。
**它回答的问题：**此刻是否有Keeper会话，何时（重新）建立，服务器是否被钉在某个Keeper主机上？
**先读：**`is_expired`、`connected_time`、`session_uptime_elapsed_seconds`对比`Uptime`（`asynchronous_metrics`）、`host`/`index`；cloud：按`hostname`的同样内容。
**健康基线：**每个配置连接一行，`is_expired = 0`，会话年龄 ≈ 服务器uptime。
**危险信号：**`is_expired = 1`（critical，HC-3.9）；长时间运行的服务器上会话年龄只有几分钟/几小时 = 最近一次过期发生在`connected_time`（P-40/P-57）；所有副本都在同一个`host`（只有leader流量）或某副本与其他对端在不同主机。
**陷阱：**< 23.8上不存在（未采集，不是证据）；`session_uptime_elapsed_seconds`每次重连都会重置，因此它只标记*最近一次*过期的时间。
**搭配文件：**`metric_log_coordination`（小时序列）、`zookeeper_log_errors_1_day`、`configuration/zookeeper.xml`。

### system.replicated_fetches
**为什么要采集：**进行中的fetch在完成或失败之前不会出现在`part_log`中。
**它回答的问题：**本副本此刻是否在拉取part，从谁那里，多大，是否卡住？
**先读：**`elapsed`、`progress`、`total_size_bytes_compressed`、`source_replica_hostname`。
**健康基线：**为空，或少量`progress`在推进的fetch。
**危险信号：**事故后出现数十个`elapsed`很长的fetch（正在追赶——意料之中，但能解释`absolute_delay`）；两个bundle中`progress`完全相同的fetch（服务器间连接卡死，HC-3.1/P-41）；`to_detached = 1`（为检查而抓取到`detached/`）。
**陷阱：**快照；为空不能证明过去一小时没发生什么——用`part_log`的`DownloadPart`看历史。
**搭配文件：**`system.replicas`（`queue_size`、`inserts_in_queue`）、`part_log`的`DownloadPart`错误（499、210、209）。

### system.distributed_ddl_queue (7天)
**为什么要采集：**`ON CLUSTER`和Replicated数据库的DDL通过Keeper执行，而不是在发起节点上通过`query_log`记录；它们的每主机状态和失败只在这里。
**它回答的问题：**哪些DDL卡住或失败，在哪个主机上，为什么；Keeper事故后DDL是否被重放？
**先读：**`status != 'Finished'`的行及其年龄；`exception_code`直方图（57、571、999、60、253）；同一段`query`文本出现在许多`entry`值下。
**健康基线：**每个条目在每个主机上都在数秒内`Finished`，`exception_code = 0`。
**危险信号：**`Active`/`Inactive`条目已存在数分钟（HC-3.11——某个主机从未接取任务，通常是一个宕机或失去会话的主机）；`CREATE OR REPLACE TABLE … .tmp.inner_id.*`上突然出现大量57 `Mapping for table with UUID … already exists`（P-57：Replicated数据库重放materialized view DDL）；571（会话在重放中途死亡）。
**陷阱：**从Keeper读取——Keeper不健康时该collector会变慢或超时（collector用户对应的`query_log`中错误码159，HC-0）；最多5000条最新行；每个 (entry, host) 一行，因此29副本集群每条语句有29行；cloud只读取一次（从每个副本看都相同）。
**搭配文件：**`system.databases`（Replicated引擎）、`query_log_details`中`user`为空的`Create`行、`text_log_histogram`的`DDLWorker`。

## 窗口聚合

### system.query_log_details_7_days
**为什么要采集：**工作负载是最先要纳入或排除的东西——流量形态、失败错误码、缓慢和重型模式以及是谁在跑；大多数事故从这里开始（第1层：流量与行为），先于数据模型或基础设施。
**它回答的问题：**服务器在窗口期内运行了什么工作负载——多少、由谁、多慢、多少内存、什么失败了？
**先读：**按`query_kind`的每小时`count`（流量形态和尖峰）；`exception_code != 0`的总数和占比（`distinct_exceptions`说明一个错误码背后藏着多少不同消息）；按`query_duration_ms/count`、`read_bytes/count`、`memory_usage/count`排的前`normalized_query_hash`；按`user`的`count`。
**健康基线：**平稳或呈昼夜节律的查询速率，异常 < 1–2%（主要是客户端错误码），慢模式可由数据量解释，没有某个用户异常主导。
**危险信号：**241占比（HC-5.3、P-10），插入上的252（P-01），202（P-20），159/394聚集（P-21），999（P-40）；某个hash的`read_bytes/count`极高或`query`中含`FINAL`/`SELECT *`（P-50/P-51）；每小时数千次插入但`written_rows/count`很小（P-02）；监控用户在榜首（P-55）。
**陷阱：****`LEFT ARRAY JOIN tables`会让每个分组按表重复一次，所有求和值重复——求和前先固定一个`tables`值（或`tables = ''`），绝不要跨行重复求和**；`type = 'QueryStart'`行不带资源数据；每个指标都是**按小时桶的求和**——除以`count`；`query`是某条查询的500字符样本；gov中没有`query`/`exception`，标识符被哈希。
**搭配文件：**`query_analysis/`（深入某个hash）、`system.tables`（热点表的schema）、`metric_log`（同一小时的内存）、`system.errors`。

### system.query_log_exceptions_1_day (v0.9, essential)
**为什么要采集：**每小时每个错误码的精确失败查询数——`query_log_details_7_days`给不了这个数，因为它会按表重复同一条查询；异常告警读取它。
**它回答的问题：**哪些错误码出现尖峰，在哪个小时？
**先读：**`type = 'ExceptionWhileProcessing'`（运行时失败）按`(time, exception_code)`的`count`；单独看`ExceptionBeforeStart`（被拒绝：202查询过多，解析和访问错误）。
**健康基线：**少量客户端错误码，跨小时平稳。
**危险信号：**一小时内某错误码 > 50（`high_exception_rate`），999 > 20（`keeper_exception_spike`，P-40），最近一小时202 > 10（`too_many_simultaneous_queries`，P-20），241（P-10），252（P-01）。
**陷阱：**只有一天（告警的回看窗口）——7天趋势在`query_log_details_7_days`；`exception`每行只有一个样本且gov中缺失；cloud对每个副本求和。
**搭配文件：**`query_log_details_7_days`（哪个模式、用户、表）、`system.errors`、`error_log_7_days`。

### system.query_log_by_hour_7_days / system.query_log_by_pattern_7_days (v0.9, onprem和cloud)
**为什么要采集：**精确的查询计数——每条查询一次，不按表重复——用于流量形态以及最慢和最重的模式；dashboard的查询面板由它们构建。
**它回答的问题：**每小时运行了多少、都是什么类型；哪个`normalized_query_hash`最慢、读取最多、失败，属于哪个用户？
**先读：**按小时：`type = 'QueryFinish'`且`exception_code != 0`的行按`(time, query_kind)`的`count`；按模式：`total_duration_ms / timed_count`（计时QueryFinish行的平均值）、`max_duration_ms`、`total_read_bytes / count`、每个`hash`的异常类型`count`。
**健康基线：**平稳或昼夜节律的速率；慢模式可由它们读取的内容解释。
**危险信号：**与`query_log_details_7_days`相同（P-50/P-51重型模式，P-55监控用户在榜首，P-10/P-20/P-40失败）。
**陷阱：**`hash`是文本（UInt64会被JavaScript舍入）；`sample_query`是一次600字符的执行；`type`把每个模式拆成QueryFinish和异常行——要汇总正确的那些；gov中不采集（那里没有dashboard）。
**搭配文件：**`query_log_details_7_days`（某个模式触碰到哪些表）、`query_analysis/`。

### system.parts_by_table (v0.9, onprem和cloud)
**为什么要采集：**每张表所有active part的汇总——`system.parts`只保留最大的50 000个，会低估由大量小part组成的表。
**它回答的问题：**哪些表承载数据，有多少part，压缩得怎么样？
**先读：**`bytes_on_disk`（从大到小）、`parts`、`rows / parts`、`data_uncompressed_bytes / data_compressed_bytes`。
**健康基线：**大小与客户预期相符；压缩比 > 3；每表part数在几十到几百。
**危险信号：**数千个part每个只有少量行（P-02）；`system`规模的意外不在这里（只含用户数据库——见`system.parts`）。
**陷阱：**仅active part；每表一行，没有分区分细（`system.parts_by_partition`）；full tier（完整分层）——在essential运行中缺失。
**搭配文件：**`system.tables`（引擎、键）、`system.parts_by_partition`、`part_log_3_days`。

### system.part_log_3_days
**为什么要采集：**后台工作的历史——它显示merge是否跟得上插入、插入有多大、哪些merge/mutation以哪个错误码失败；单看`system.merges`只是一个瞬间。
**它回答的问题：**后台工作在窗口期内做了什么——创建、合并、变更、抓取、删除part——有失败吗？
**先读：**`event_type × merge_reason`汇总；`error != 0`且带`exception`/`distinct_exceptions`的行；每表每小时的`NewPart`对比每小时`MergeParts`；NewPart（插入大小）和MergeParts（merge大小）的`size_in_bytes/count`；每表的`peak_memory_usage`最大值。
**健康基线：**NewPart ≈ 插入次数，MergeParts占稳定比例，到处`error = 0`，声明TTL的表上有TTL merge，merge结果大小在增长（层级上升）。
**危险信号：**某个分区上重复出现MergeParts `error = 241`（P-11），`error IN (40, 226, 1001, 49)`（P-31/P-30），每小时数千个KB级part的NewPart（P-02），`DownloadPart`错误（P-44），TTL表上没有`TTLDeleteMerge`（P-18），`MutatePart`错误53/70/117（P-15/P-32），`system.*`表占据最大merge内存（P-17）。
**陷阱：**从v0.9起所有版本上，汇总都是按**1小时**桶且带`hostname`（gov中哈希）——更早的bundle在23.11以下用12小时桶；除以`count`；刻意不采集`part_name`；更早的bundle有未起别名的`any(exception)`列；gov丢弃异常列并把`table_name`拆成哈希的`database`/`table`。
**搭配文件：**`system.parts`、`system.merges`、`system.mutations`、`asynchronous_insert_log`。

### system.metric_log_7_days
**为什么要采集：**随时间变化的内存和后台池饱和度——这两个计数器能区分“服务器过载”和“某个查询行为不端”；池值被钉在池大小（或原始指标中 > 256）意味着系统过载 *（经验值）*。
**它回答的问题：**服务器内存和后台池逐小时表现如何——dashboard把fetch / schedule / merge池画在各自大小旁边（v0.7）；内存、公共池和Keeper计数器只从这个文件读取。
**先读：**每小时`avg_memory_tracking_bytes`对比RAM/cgroup/`max_server_memory_usage`；`max_merge_pool_tasks`对比`background_pool_size`（默认16）；`avg_/max_fetch_pool_tasks`对比`background_fetches_pool_size`（默认16）以及`avg_/max_schedule_pool_tasks`对比`background_schedule_pool_size`（默认512）——cloud中读取`max_replica_avg_fetch_pool_tasks` / `max_replica_avg_schedule_pool_tasks` / `max_replica_avg_merge_pool_tasks`（最差副本的小时平均值；普通的`avg_*`是集群均值，会掩盖空闲节点中某个饱和节点）——大小在`system.server_settings`（23.3+）或`system.settings`中；`avg_/max_common_pool_tasks`；`zk_transactions`基线和尖峰；`zk_hw_exceptions`。
**健康基线：**内存远低于限制的80%且呈昼夜形态，merge池大多数小时低于其大小，`zk_hw_exceptions = 0`。**磁盘**（每个计数器都跨线程求和：是总量，不是设备测量）：`sum(ProfileEvent_OSReadBytes)` / `sum(ProfileEvent_OSWriteBytes)`是块设备实际服务的数据，不含页缓存，对所有本地卷求和——除以覆盖秒数，得到该小时的平均存储流量；`sum(ProfileEvent_ReadBufferFromFileDescriptorReadBytes)` / `…WriteBufferFromFileDescriptorWriteBytes`是ClickHouse请求的量；`sum(ProfileEvent_DiskReadElapsedMicroseconds)` / `…DiskWriteElapsedMicroseconds`是读写调用内的线程时间，包含缓存命中的调用、重叠调用会累加——这是上下文，绝不是吞吐量的分母；`sum(ProfileEvent_OSIOWaitMicroseconds)`除以覆盖秒数，是阻塞在存储上的平均线程数。`metric_samples`和`span_s`（首次到末次采样）是原始覆盖度：覆盖秒数 = `metric_samples` × 采样间隔，bundle不说明采样间隔，§4a从未中断的小时中推导。版本缺少该计数器的服务器上不存在这些列（它们按正则选取），数据在对象存储上的服务上接近零——那里由`metric_log_coordination_3_days`中的S3计数器承担同等作用。
**危险信号：**内存连续数小时 > 80%（HC-5.1），逐日下限上升（P-12），merge池数小时被钉在其大小（HC-2.7、P-01），存储流量可映射到某个卷（单个本地数据盘，或该卷自身的遥测）且数小时接近其配置吞吐量、同时merge变慢、阻塞在I/O上的线程多于安静时段（设备是瓶颈，HC-4.6——应提高卷吞吐量而不是池大小；有多个本地卷时，求和后的计数器无法说明是哪一个），fetch池均值 ≥ 其大小的90%或schedule池在最近24 h中有3个小时 ≥ 95%（HC-2.13/2.14，告警`fetch_pool_saturated` / `schedule_pool_saturated`——SharedMergeTree上这是part传播延迟，P-59），任何`zk_hw_exceptions`（HC-3.5、P-40），`zk_transactions` ×5（大量小插入/mutation造成的Keeper负载）。
**陷阱：**小时**平均值**隐藏短时尖峰；`avg_memory_tracking_bytes`是被跟踪的分配量，不是RSS（RSS通常更高）；需要启用`system.metric_log`（默认已启用）；`OSReadBytes` / `OSWriteBytes` / `OSReadChars` / `OSWriteChars`计数器从`/proc`读取，**在非Linux服务器上为0**（在macOS上实测），此时bundle无法测量设备：`ReadBufferFromFileDescriptor*`是逻辑I/O，包含由缓存服务的流量，`Disk*ElapsedMicroseconds`是重叠的调用时间——缓存驻留的工作负载会把两者都推高，而设备流量几乎为零。不要把零读成设备空闲；应索取OS级磁盘遥测。采样间隔可配置（`collect_interval_milliseconds`，默认1 s）：绝不要假定每秒一个样本，若没有任何小时有两个或以上样本，就不要给出速率。**`OSIOWaitMicroseconds`需要小心**：它和其余计数器一样是每样本增量，但服务器启动后的第一个样本可能携带累计总量——在某个Cloud副本上实测，一个1秒样本包含3.5e12 µs，即40天的等待。只有在有证据表明该小时发生了启动时才把该小时视为不可靠——样本数少于其跨度和间隔所暗示的数量（§4a中的`missed_samples`）、`uptime:`头部、`logs/`中的启动——绝不能因为值高就怀疑：当许多线程同时阻塞时，等待时间合法地超过一小时。它来自内核的每线程延迟统计，也可能全程为0（实测：某个Linux容器每小时写入约1 GB，每小时均为0）——0表示未测量，不是没有等待。数据在对象存储上的服务中，其余增量按设计约为0（实测：三天内53 000个样本中有15–49个非零样本）。
**搭配文件：**`host_info.memory`、`query_log_details`（哪些小时繁忙）、`part_log.peak_memory_usage`；`metric_log_by_host_3_days`提供每个池列的每主机形式和实时大小。

### system.asynchronous_insert_log_7_days
**为什么要采集：**当`wait_for_async_insert = 0`时，异步插入会静默失败；flush日志是发现丢失的flush或缓慢的MV定向flush的唯一地方。
**它回答的问题：**如果使用异步插入——flush是否成功，有多大，客户端等多久？
**先读：**`status`汇总（`Ok`、`ParsingError`、`FlushError`）；每表每小时`flushes`；`total_rows/flushes`（达到的批大小）；`avg_flush_ms`/`p90_flush_ms`。
**健康基线：**全部`Ok`，每次flush数千行，p90 flush在一两秒内。
**危险信号：**`FlushError`（若`wait_for_async_insert = 0`则数据丢失，P-33），`ParsingError`（客户端发送坏行），`p90_flush_ms`以秒计（HC-7.2——MV扇出，P-34），`total_rows/flushes`很小（缓冲区超时flush且行数很少）。
**陷阱：**< 22.10上或未使用异步插入时不存在（不是问题）；`total_rows`仅在 ≥ 23.4；gov中名称被哈希。
**搭配文件：**`query_log_details`的`Insert`行、`system.tables`（目标表上的MV）、`part_log`目标表上的NewPart。

### system.query_views_log_3_days
**为什么要采集：**这是记录materialized view自身执行的唯一地方。一个“成功”但什么都没写的MV在`query_log`中不可见。抛异常的MV默认会让父INSERT失败——`query_log`会带有`… while pushing to view db.mv`——但在`materialized_views_ignore_errors = 1`下，INSERT报告成功，只有本表（以及一条`text_log`的 “Error is ignored” 警告）记录它。两种情况下，基础part都在视图运行前已提交。
**它回答的问题：**每次插入是否都触发了每个MV，是否有MV在基础part已提交后失败，每个MV在内存和时间上花多少？
**先读：**`status`汇总（`QueryFinish`对比`ExceptionWhileProcessing`）；每个视图的`zero_write_executions / executions`；同一源表上各视图每小时`written_rows`的对比；`exception_code`及其`exception`；每个视图随时间的`median_peak_memory_usage`和`max_view_duration_ms`。
**健康基线：**每行`QueryFinish`且`exception_code = 0`；`zero_write_executions`接近0，或是一个可由MV的WHERE子句解释的稳定比例；每视图内存跨天平稳。
**危险信号：**任何`ExceptionWhileProcessing`——基础插入已提交而MV目标没有（P-36）；一个过去会写入的视图出现`zero_write_executions ≈ executions`且`read_rows > 0`（P-35）；某个视图每小时`written_rows`与同一源表上的兄弟视图分化（P-35）；升级边界前后`median_peak_memory_usage`阶跃上升数倍而`written_rows`持平（P-54）；`max_view_duration_ms`主导插入路径（P-34）。
**陷阱：****在默认设置下，失败的INSERT会给管道中*每个*视图都标上`ExceptionWhileProcessing`和同一条消息，而不只是出错的那个**——应把失败归因到文本中命名的视图（“while pushing to view X”），并统计该视图的行，而不是兄弟视图的总和；在`materialized_views_ignore_errors = 1`下只有抛异常的视图会被标记；`ExceptionWhileProcessing`行上的`written_rows`统计的是从未落地的行——插入已回滚——因此只从`QueryFinish`行汇总写入；汇总是按**1小时**桶，所以求平均值要除以`executions`；对于过滤型MV，`zero_write_executions > 0`是正常的，因此始终把它作为相对该视图自身早先桶的比率来读；不采集`view_uuid`，因为服务器会向其中写入零UUID——对于不带`TO`声明的MV，`view_target`是`db.`.inner_id.<uuid>``，且视图重建时它*确实*会变；文件缺失或为空意味着未配置`<query_views_log>`或`log_query_views = 0`，这是采集缺口，不是健康信号；gov没有`exception`文本，并把标识符拆成哈希的database/table对。
**搭配文件：**`system.tables`（MV DDL和依赖链）、源表的`query_log_details` `Insert`行、目标表的`part_log` NewPart、`asynchronous_insert_log`（flush延迟受MV扇出影响）。

### system.metric_log_coordination_3_days
**为什么要采集：**`metric_log_7_days`只保留固定且跨版本稳定的少量列；本文件逐小时采集该版本导出的所有Keeper、对象存储、文件系统缓存和复制计数器，因此即使没有查询失败，也能看到*依赖项*的故障。
**它回答的问题：**哪些小时Keeper停止应答，S3是否开始返回错误，缓存是否抖动，fetch是否失败——这些小时与工作负载如何对应？
**先读：**每小时`sum(ProfileEvent_ZooKeeperHardwareExceptions)`和`sum(ProfileEvent_ZooKeeperTransactions)`（发生故障 = 前者爆炸而后者崩塌）；`max(CurrentMetric_ZooKeeperSession)`（某小时为0 = 完全没有会话）；`sum(ProfileEvent_S3ReadRequestsErrors)`、`…ReadBufferFromS3RequestsErrors`、`…S3WriteRequestsErrors`；`sum(ProfileEvent_ReplicatedPartFailedFetches)`；`max(CurrentMetric_ReadonlyReplica)`；`sum(ProfileEvent_FailedInsertQuery)`；`sum(ProfileEvent_RejectedInserts)` / `DelayedInserts`。
**健康基线：**每小时硬件异常为0，事务量呈稳定的昼夜曲线，S3错误列为0或极小，`ReadonlyReplica`为0。
**危险信号：**任何小时硬件异常达到数千或更多（HC-3.8 Keeper健康测试——告警`keeper_health` / `keeper_connection_blips`，P-57）；`ReadonlyReplica`最大值 > 0（HC-1.4）；在107/`NoSuchKey` finding之前数小时S3错误列上升（HC-4.10、P-58）；`ReplicatedPartFailedFetches`突发（P-41）。
**陷阱：**列名带有聚合函数——`"sum(ProfileEvent_ZooKeeperTransactions)"`——且列集合**依赖版本**（缺少某列意味着该版本没有该计数器，而不是它为0）；值是每小时的，cloud模式下对所有副本求和/取最大；`CurrentMetric_*`的小时**最大值**会掩盖小时内的下跌。
**搭配文件：**`metric_log_7_days`（内存和池）、`part_log_3_days`（merge是否停了？）、`query_log_details`（那些小时有哪些错误码）、`zookeeper_connection`。

### system.metric_log_by_host_3_days（仅cloud，v0.7）
**为什么要采集：**`metric_log_7_days`把副本折叠成集群总量（外加最差副本的池平均值）；本文件保留主机，因此finding可以定位到某个副本的某个小时——哪个副本的池被钉住、钉在什么**实时**大小、Keeper延迟多少，以及fetch执行器是否在丢弃工作。
**它回答的问题：**按主机和小时：fetch / schedule / merge池占用对比该主机的实时池大小；实时池大小是否变化（增加与重新加载或带新配置重启一致；减少只可能是重启——池是`IncreaseOnly`）；在SharedMergeTree上，被选中抓取的part数对比尝试的fetch次数、花在fetch上的时间、leader选举；CPU和active part（追赶形态）；每主机的Keeper事务、异常和等待时间。
**先读：**每主机`avg_fetch_pool_tasks`对比`max(CurrentMetric_BackgroundFetchesPoolSize)`，`avg_schedule_pool_tasks`对比`max(CurrentMetric_BackgroundSchedulePoolSize)`（HC-2.15）；大小列发生变化的任何小时（设置切换——HC-2.15说明如何读其方向）；每小时`sum(ProfileEvent_SharedMergeTreeDataPartsFetchAttempt)`（平坦 = 达到上限；×3–5 = 追赶）对比`sum(ProfileEvent_SharedMergeTreeSelectPartsForRendezvousFetchParts)` + `…CoordinatedFetchParts`（尝试 / 选中 < 0.5 = 丢弃）；`sum(ProfileEvent_SharedMergeTreeHandleFetchPartsMicroseconds)` / 尝试次数 = 每次fetch的秒数；`sum(ProfileEvent_SharedMergeTreeVirtualPartsUpdatesLeaderSuccessfulElection)` / 3600 = 选举/s；每主机`zk_wait_us / zk_transactions`对比全集群中位数（HC-3.17）；变化后的`cpu_us`和`max_parts_active`。
**健康基线：**每个主机上每个池都远低于其实时大小，尝试 ≈ 选中，选举/s在几十，各主机Keeper ms/tx都在中位数的2× 以内，没有大小切换（或有一次且可解释）。
**危险信号：**某主机最近24 h中有 ≥ 3个小时池达到其实时大小的 ≥ 90%（fetch、merge）/ 95%（schedule）（HC-2.15、P-59）；尝试 ≪ 选中；某些主机schedule被钉住而fetch空闲，另一些主机相反（schedule池在限制fetch）；选举 ≥ 100/s（HC-2.16）；某主机的Keeper延迟远高于对端（HC-3.17）；大小增加后，`inserts_in_queue`已到0数小时而`cpu_us`和merge池仍偏高（别处还有积压，或需求高于新上限）。
**陷阱：**正则选取的列在名称中带有聚合函数，且**依赖版本**——缺失意味着该版本没有该计数器，而不是0——SharedMergeTree计数器在ReplicatedMergeTree上为0；fetch选择计数器在26.6+ SMT上得到确认，更早的SMT构建可能没有；`cpu_us`是所有线程的求和——对于**完整**的小时桶，利用率%= `cpu_us / (3 600 × 1e6 × cores) × 100`（首尾桶是不完整的）；`avg_*`是小时平均值，会掩盖小时内尖峰；大小列缺失时实时大小回退到`server_settings`；单节点采集只有一个主机（此时等同于`metric_log_7_days`）。
**搭配文件：**`metric_log_7_days`（集群视角，7天）、`system.replicas`（该主机上哪些表落后）、`system.merge_tree_settings`（两个SMT旋钮）、`system.server_settings`、`zookeeper_connection`（慢主机位于哪个Keeper成员上）、`part_log_3_days`（`DownloadPart`计数作为上限测试的第二来源）。

### system.text_log_histogram_1_day
**为什么要采集：**2000行的`text_log`切片只包含最新几行；在日志嘈杂的服务器上只有几分钟。直方图按小时×级别×组件粒度覆盖一整天。
**它回答的问题：**Warning/Error数量何时变化，在哪个组件，典型行说了什么？
**先读：**每小时Error和Fatal的`count`；只出现在事故小时的`logger_class`值；它们的`example`。
**健康基线：**来自少数logger的平稳低Warning基线，Error计数接近零，没有Fatal。
**危险信号：**Error `count`是安静小时的10× 的小时（HC-11.5）；事故典型类别：`ZooKeeperClient`、`DDLWorker`、`DatabaseReplicated`、`InterserverIOHTTPHandler`、`MergeTreeBackgroundExecutor`、`*::MetaInKeeper`/`DiskS3`、带`Code: 107`的`executeQuery`；`BackgroundSchedulePool`的 “Temporarily pause scheduling” 达数千条（merge/fetch被限流）。
**陷阱：**`logger_class`是归一化结果（表相关logger折叠为组件；普通logger保持原样）；`example`是每个单元格*一条*行，不是最差的那条；gov保持类别可读，掩码`<disk>::`前缀，只对无法识别的`db.table`回退和示例做哈希。
**搭配文件：**`system.text_log`（确切的最新行）、`logs/*.err.log`（grep该小时）、`query_log_details`（相同小时）。

### system.error_log_7_days (≥ 24.8)
**为什么要采集：**`system.errors`是重启以来的累计计数器；`error_log`是它的历史——服务器中任何位置产生的每个错误码，包括从不接触`query_log`的后台线程，逐小时记录。
**它回答的问题：**每个错误码何时开始和停止；999/242/252/107是否在夜间发生且没有对应查询？
**先读：**每小时999、242、252、107、499、241的`errors`；`remote = 1`的行（从其他服务器收到的错误——interserver/分布式）；某个错误码首次出现的小时。
**健康基线：**预期错误码的平稳低基线（探测产生的60 UNKNOWN_TABLE、516认证噪声），没有突发。
**危险信号：**与Keeper健康测试小时对齐的突发（HC-3.8）；每个小时都以稳定速率出现的错误码（重试循环——P-41/P-47）；`remote = 1`突发（对端失败，86/221）。
**陷阱：**低于24.8或禁用`<error_log>`时不存在；计数是事件数，不是不同事故数；错误*名称*是ClickHouse常量，不是消息——与`system.errors.last_error_message`和`text_log`搭配。v0.9中最多20 000行，依赖Keeper的错误码（999、242、319、571、252）优先：恰好20 000行意味着其他错误码丢失了最新小时，而Keeper错误码从不丢失。
**搭配文件：**`system.errors`（消息 + trace）、`query_log_details`（哪些查询）、`part_log`（哪些merge）、`metric_log_coordination`。

### system.text_log_keeper_1_day
**为什么要采集：**会话生命周期行（`Session expired`、`Finalizing session`、`Connected to ZooKeeper at <host>`、`Trying to establish a new connection`）是Information/Debug级别，因此只含Warning及以上的`text_log`切片从来不会有它们；它们给出会话丢失的*分钟*以及服务器迁往的Keeper主机。
**它回答的问题：**本服务器究竟何时丢失并重新获得Keeper会话，频率如何，连到哪个主机？
**先读：**每小时`marker`计数（`session_expired`、`session_finalized`、`reconnecting`、`connected`）、`first_seen`/`last_seen`、`connected`的`example`（host:port）。
**健康基线：**没有行，或只在服务器启动时有一条`connected`。
**危险信号：**Keeper健康测试（HC-3.8）标记的小时内出现`session_expired`/`reconnecting`；反复重连到*不同*主机（leader抖动）；`connection_loss`/`operation_timeout`突发。
**陷阱：**只有一天（受`text_log`量限制）；`text_log`级别高于Information的服务器不会显示任何内容——要说明这一点；gov对示例做哈希。
**搭配文件：**`metric_log_7_days` / `metric_log_coordination`（小时序列）、`zookeeper_connection`（当前会话）、`configuration/zookeeper.xml`（存在哪些主机）。

### system.zookeeper_log_errors_1_day（仅在启用时）
**为什么要采集：**`zookeeper_log`是每个Keeper请求和响应的客户端记录——也是繁忙集群上最大的system表（每个请求*和*每个响应一行；每天数十GiB）。在collector中全量聚合它不现实，因此本文件只保留**失败的响应**：说明*哪些*操作因*哪个*Keeper错误失败，以及影响多少个会话。
**它回答的问题：**哪些Keeper操作失败，何时，错误是什么；影响了多少个会话？
**先读：**每小时`error` ∈ `ZSESSIONEXPIRED` / `ZCONNECTIONLOSS` / `ZOPERATIONTIMEOUT`（丢失）对比`ZNONODE` / `ZNODEEXISTS` / `ZBADVERSION`（正常控制流）；失败调用的`op_num`（`Multi` = part提交，`Create`/`Set` = 写，`Get`/`List` = 读）；`sessions_affected`；失败调用的`max_duration_ms`。
**健康基线：**只有控制流错误，速率稳定；`sessions_affected`为1。
**危险信号：**Keeper健康测试标记的小时内出现丢失类错误（HC-3.10）；`ZOPERATIONTIMEOUT`出现在`ZSESSIONEXPIRED`*之前*（先饱和）；`sessions_affected > 1`（服务器在该小时内经历了多个会话）。
**陷阱：**文件缺失 = 未配置`<zookeeper_log>`，不是健康状况；成功量和延迟刻意**不在**这里——用`metric_log_coordination`（每小时`ZooKeeperTransactions`、`ZooKeeperWaitMicroseconds / ZooKeeperTransactions`）和`text_log_keeper_1_day`看会话更替；即使只取错误，collector也要扫描整天的两个1字节列，因此在超大集群上可能触发collector超时（159）；所有版本上持续时间都是ms（≥ 24.3从微秒转换）。
**搭配文件：**`metric_log_coordination`（同一小时来自ProfileEvents）、`text_log_keeper_1_day`、`zookeeper_connection`、`configuration/zookeeper.xml`。

### system.aggregated_zookeeper_log_by_hour_2_days (≥ 26.3)
**为什么要采集：**其他每个Keeper文件只说明Keeper*本身*忙、慢或失败；这个文件说明**服务器里的谁**在发请求。`system.aggregated_zookeeper_log`（自25.9起默认开启，保留30天——与通常关闭的`zookeeper_log`不同）按会话、父路径、操作和`component`（发出请求的ClickHouse代码位置）对本服务器发出的每个请求分组。按组件×操作逐小时读取两天。
**它回答的问题：**哪个组件发送哪些Keeper操作，多少，多慢，带什么错误；cloud中哪个副本贡献最多？
**先读：**按`component`求和的`ops + in_multi`（前几个通常承载几乎全部）；对头部组件按小时的同样内容（热循环在若干小时远高于其平常水平）；每小时每个`operation`的`avg_latency_us`；Remove / TryRemove / RemoveRecursive上的`errors`映射（`ZNONODE`占比）。
**健康基线：**流量分散在清理、重启和更新队列的线程、`DDLWorker`和part提交上，逐小时稳定；加权延迟在个位数到十几毫秒；错误是少量`ZNONODE` / `ZNODEEXISTS`占比（正常控制流）。
**危险信号：**某组件 ≥ 60%且峰值小时 ≥ 其平常小时的10×（HC-3.22、P-68）；某操作平均 ≥ 1 s（P-66 / P-67）；remove大多返回`ZNONODE`（P-62）；某组件的错误中出现`ZCONNECTIONLOSS` / `ZSESSIONEXPIRED`（P-57——用`metric_log_coordination`匹配小时）。
**陷阱：**低于26.3时不存在——不能说明任何事（该表从25.9起存在，但26.3之前子请求与独立请求共享桶并携带整个Multi的延迟，因此工具跳过它）；`ops`统计独立请求（一个Multi算一次），`in_multi`统计Multi / MultiRead内部的子请求——要衡量“多少工作量”就把两者相加，要衡量“多少次往返”就只用`ops`；延迟以**微秒**计，按计数加权，只来自独立请求（子请求报告0）；每台服务器只记录**自己的**会话——同一Keeper的其他客户端不可见，因此占比是本服务器的，不是Keeper的；cloud行对所有副本求和，并带`replicas`、`busiest_replica`和`busiest_replica_ops`——每副本细节在按分钟文件；窗口为2天（`-from` / `-to`可移动；表保留30天）。
**搭配文件：**`…_by_minute_1_day`（何时）、`…_paths_1_day`（何处）、`metric_log_coordination_3_days`（每小时Keeper等待，客户端侧）、`text_log_keeper_1_day`、`replication_queue`（循环任务在重试什么）、`keeper/<member>.txt`。

### system.aggregated_zookeeper_log_by_minute_1_day (≥ 26.3)
**为什么要采集：**同一张表的分钟分辨率——Keeper对本服务器（cloud中为**每个**副本）**何时**变慢、变忙或开始失败，这些被小时数字抹平的细节：持续数分钟的延迟跃升、重启突发、某个副本流量崩塌。
**它回答的问题：**哪些分钟请求变慢、尖峰或失败；是一个副本还是所有副本？
**先读：**每分钟加权`avg_latency_us`（cloud中按`hostname`）及每个副本的中位数；每分钟`ops`对比当天中位数；`loss_failures`（连接丢失、会话过期、操作超时）分钟；最慢单个组的`worst_group_latency_us`。
**健康基线：**每请求约7–20 ms，平稳；`failed`是小而稳定的数字；`loss_failures`为0。
**危险信号：**分钟 ≥ 100 ms（HC-3.21）——而从约15升到约30 ms就已值得注意，它曾领先一次故障11分钟；某副本以秒计而其他副本保持在约10 ms（P-65）；某分钟请求量 ≥ 平常的10×（P-64重启突发）；某副本的`ops`降到几乎为零而其兄弟副本仍在继续（P-65的卡死副本变体）；任何`loss_failures`（P-57）。
**陷阱：**低于26.3时不存在；窗口1天——更早的事故需要`-from` / `-to`；onprem/gov文件没有主机名（每个bundle一台服务器）；延迟只来自独立请求。
**搭配文件：**`text_log_keeper_1_day`（同一分钟的会话标记）、`zookeeper_connection`（每个会话位于哪个成员）、`metric_log_by_host_3_days`（HC-3.17，每主机小时视图）、`execution_log` uptime（重启）。

### system.aggregated_zookeeper_log_paths_1_day (≥ 26.3)
**为什么要采集：****Keeper中的哪些位置**收到请求以及哪些子树在增长——这是Keeper内存耗尽（znode净增长）和读风暴（一个子树吃掉一切）背后的两个问题。每个路径类别一行：当天请求最多的50个类别加上净创建最多的50个。
**它回答的问题：**哪些Keeper路径承担负载；哪些在增长（创建 − 删除）；其中多少来自Multi内部？
**先读：**按`ops`的头部行及其`in_multi`占比和`raw_paths`；按`net_creates`的头部行，`creates`对比`removes`（仅成功请求——返回`ZNODEEXISTS`的`CreateIfNotExists`不算创建）；`removes`中的去重类别（`…/blocks`、`…/async_blocks`、`…/deduplication_hashes`）。
**健康基线：**按表的类别（`/clickhouse/tables/…/<table>`、`…/replicas/<r>`、`…/blocks`、`…/log`）分担负载；一天内创建和删除大致平衡；对象存储磁盘上有`_objects`类别但不占主导。
**危险信号：**`net_creates`达数百万且creates ≥ removes的10×（HC-3.23、P-63——这是请求平衡，因为一次`RemoveRecursive`会删除整棵子树：用`keeper/<member>.txt`中的`zk_znode_count`确认，最好跨两个bundle）；某个类别 ≥ 请求的50%、大多为`in_multi`、位于`/…/mergetree/_objects`（P-64）；去重类别占据大部分removes（P-62）。
**陷阱：**低于26.3时不存在；纯数字路径段被替换为`N`，因此数千个孩子（`…/_objects/15536/.dir`）形成一个类别（`raw_paths`说明有多少），分片编号也会合并（`/tables/01/` → `/tables/N/`），而表UUID和名称保留；Multi / MultiRead信封不带路径，不在这里；`ops`包含子请求（读风暴就在这里），`avg_latency_us`不包含；占比是相对于已采集类别的，不是整个Keeper；gov bundle用`shape`（保留ClickHouse自己的路径词，其余为`*`）和`path_id`（加盐哈希）代替`path`；窗口1天。
**搭配文件：**`keeper/<member>.txt`（`zk_znode_count`、`zk_approximate_data_size`——增长产生的规模）、`part_log`（每次插入创建多少part）、`system.parts` / `system.columns`（每个part多少文件）、`…_by_hour_2_days`（某个类别背后的组件）。

### system.blob_storage_log_7_days（≥ 23.11，仅在启用时）
**为什么要采集：**对象存储操作不会在`part_log`中留下痕迹；这里能看到被删除或从未上传的blob。
**它回答的问题：**上传是否失败；blob何时被删除；删除是否发生在如今失败的读取之前？
**先读：**`failed = 1`的行及其`example_error`；每小时`Delete`计数对比基线；每小时`Upload`/`MultiPartUpload*`字节数（写吞吐量）。
**健康基线：**到处`failed = 0`，删除占上传的稳定比例（merge替换part）。
**危险信号：**上传失败（HC-4.9——part的数据从未到达bucket；后续读取以107/`NoSuchKey`失败）；删除突发之后同一批表出现107错误（P-58）；提及403/凭据的错误（策略或密钥轮转问题，不是数据丢失）。
**陷阱：**文件缺失 = 未配置该表；`bytes`不含`Delete`行（其`data_size`是哨兵值）；日志按key命名blob，而不是按表——负责人用`system.remote_data_paths`把key映射到part，而bundle不包含该表（太大）。
**搭配文件：**`system.disks`/`storage_policies`（哪个磁盘）、`query_log_details`按表的107、`system.metrics`每副本缓存对象数。

### system.text_log（24小时切片，≤ 2000行，严重级别优先，每个logger ≤ 200行）
**为什么要采集：**服务器最近的警告和错误全文——Keeper会话丢失、merge失败、重试循环和 “Too many parts” 都会连同相关对象出现在这里。
**它回答的问题：**服务器自己最近抱怨了什么？
**先读：**`min(event_time)`——如果它只比`max(event_time)`早几分钟，说明触达上限，该切片是快照而非一天（HC-11.4）；然后看`logger_name`直方图；再看最新的20条Error/Fatal行；最后看`message`中的`Code: NNN`直方图。
**健康基线：**每小时少量Warning，没有Error/Fatal，时间跨度覆盖整天。
**危险信号：**相同消息重复（重试循环——计数，不要逐条读），`Code: 999`/`Session expired`（P-40），merge上的`Code: 241`（P-11），`broken`/`checksum`（P-31），`Too many parts`（P-01），`Not executing … because`（P-04），S3 `SlowDown`（P-45）。
**陷阱：**gov中`logger_name`/`message`被哈希（只有量和时间保留）；Debug/Trace按设计排除，因此缺失什么都不能证明；OSS中`text_log`默认禁用——文件缺失通常就是这个原因。
**搭配文件：**`system.errors`、`logs/`，以及用`--collect-text-log`获取更大但有界的切片。

### text_log_<ts>（可选`--collect-text-log`切片）
**为什么要采集：**当默认的24 h/2000行切片太窄时，在选定级别上取一个有界切片，可以捕获事故的开端，而无需导出整张表。
**它回答的问题：**在明确窗口内，服务器以 ≥ 选定级别记录的全部内容。
**先读：**按`level`、按`logger_name`、按`message_format_string`（≥ 23.1——折叠变体）的`count()`；逐分钟计数以找到开端；然后看开端处的前20条Error行。
**健康基线：**不适用——之所以采集它，是因为发生了事情。
**危险信号：**事故本身是什么就是什么；寻找**第一条**错误，而不是最响的那条。
**陷阱：**可能有数十万行——先聚合；文件名中的时间戳是UTC。
**搭配文件：**同一窗口的`query_log_details`、级别低于所选级别的`logs/`行。

## 其他产物

### query_analysis/（12个文件，`--query-id` / `--normalized-query-hash`）
**为什么要采集：**要解释某条查询的慢，需要它的ProfileEvents与同形态的快运行对比、它的裁剪日志行以及它触碰的表的DDL——这些常规采集都没有（常规采集只有每个hash的小时汇总）。
**它回答的问题：***这条*查询为什么慢或失败——墙钟时间花在哪里，慢运行与快运行有何不同？
**按此顺序先读：**
1. `query_details` → `tables`、`is_initial_query`、`initial_user`、`query_kind`、`read_rows`、`read_bytes`、`query_duration_ms`、`memory_usage_human`、`peak_threads_usage`。`tables`以`_table_function.`开头（s3Cluster、s3、url、remote）意味着不涉及MergeTree part；`is_initial_query = 0`意味着该hash是**工作节点子查询**——工具挑的是最慢的*分片*，其他“执行”是同一语句在其他副本上的份额（在`text_log_full`中找父查询：`initial_query_id: …`）。
2. `profile_events` → 构建时间预算：`query_duration_ms`（墙钟——`RealTimeMicroseconds`是跨线程求和的，不要当墙钟用）、`OSCPUVirtualTimeMicroseconds`（使用的CPU）、`OSCPUWaitMicroseconds`（想要但未被调度的CPU——当 ≈ 已用CPU时为限流）、`NetworkSendElapsedMicroseconds`（发往发起方/客户端的行）、`ParquetFetchWaitTimeMicroseconds` / `ReadBufferFromS3Microseconds` / `S3ReadMicroseconds`（对象存储等待）、`DiskReadElapsedMicroseconds`、`SelectedMarks`/`SelectedParts`/`SelectedRows`、`MemoryAllocatedWithoutCheckBytes`（跟踪器未看到的分配——Arrow/Parquet解码）。
3. `executions_timeline` + `hash_by_host` →**是多次运行还是多个分片？**不同主机同一分钟且`read_rows`与`query_duration_ms`成正比 = 一条分布式语句的分片（数据倾斜）；在任一主机上跨数小时分布 = 独立运行（回归或争用）。
4. 然后才看`profile_events_compare`（按`|delta|`排的头部；对运行有意义，对分片有误导）、`text_log_parts`（“Selected N/M parts … K marks”——裁剪）、`tables_for_query`（DDL、大小）、`text_log_full`（开端：`Reading object …`大小、`Selected …`、相邻行之间最长的间隔 = 时间花在哪里）、`failed_over_time`/`failed_queries`（何时以及如何失败）。
**健康基线：**`SelectedParts/Marks`中慢 ≈ 快；墙钟时间可由读取的行数解释；CPU等待 ≪ CPU使用；网络发送占小头。
**危险信号：**`SelectedParts = total parts`（无裁剪，P-50）；`ReadBufferFromS3*`/`S3GetObject`增量巨大（冷缓存 / S3，P-45）；`OSCPUWait ≈ OSCPUVirtualTime`（被CPU限流的pod，P-22）；工作节点子查询上`NetworkSendElapsed` > 墙钟的50%（发起方侧插入，P-56）；内存增量（P-52）；某主机p95 ×10（P-44/P-54）；单个`Reading object`比其他大数倍（文件倾斜，P-56）。
**陷阱：**gov中从不包含；`executions_timeline` ≤ 10 000行；内含完整查询文本——分享前先过隐私检查；**空文件往往表示“不适用”，而不是“未采集”**：表函数查询的`text_log_parts`和`tables_for_query`为空，从未失败的查询的`failed_*`为空——要说明原因，而不是标记为缺口；当hash是工作节点子查询时，自动推导出的“快”执行只是最小的分片。
**搭配文件：**`query_log_details`（该hash的历史和父语句的hash）、`system.tables`、`metric_log`（那个小时服务器是否繁忙）。

### host_info.json（onprem默认；gov中从不包含）
**为什么要采集：**THP、overcommit、cgroup限制、打开文件数限制和可用内存解释了自管理事故的很大一部分，而且从数据库内部看不到。
**它回答的问题：**这是什么机器，是不是正确的那台，ClickHouse关心的OS旋钮是否设置得合理？
**先读：**`os.hostname`对比别处见到的主机名（这是服务器还是运维的笔记本？）；`notes`；`cpu.logical_cpus`和`load_avg_1_5_15`；`memory.available_bytes`、已用swap；`clickhouse_relevant_tunables`（THP、overcommit、`max_map_count`、`nofile`、cgroup限制）；数据挂载的`disks[]`；`top_processes_by_rss`。
**健康基线：**THP为`madvise`/`never`，overcommit为`0`，`nofile` ≥ 500000，可用内存 ≫ 2 GiB，负载 < 核数，未使用swap，ClickHouse是唯一的大RSS。
**危险信号：**THP为`always`，overcommit为`2`，可用内存 < 2 GiB（HC-10.1–10.3——ClickHouse启动时警告的三项），cgroup限制 ≪ RAM（HC-10.4），`nofile`偏低（HC-10.5），负载 ≫ 核数（HC-5.5），使用swap（HC-5.6），同机进程吃掉RAM（HC-5.7）。
**陷阱：**`available: false` + `notes` = 降级（通常是在服务器之外采集的）——**忽略它并说明**；`disks[]`包含tmpfs/overlay；值以字节为单位，load average是字符串。
**搭配文件：**`metric_log`内存、`system.disks`、`logs/`启动警告。

### logs/（onprem默认；gov中从不包含）
**为什么要采集：**重启、启动警告、致命堆栈和事故的*第一条*错误只在服务器日志文件中；系统表在重启后会丢失它们。
**它回答的问题：**服务器自己的叙述——重启、启动警告、致命错误、重试循环、事故的第一条错误。
**先读：**`### support-diagnostic: TRUNCATED`头部（开头是否缺失？）；`grep -c '<Fatal>'`；`Starting ClickHouse`/`Ready for connections`/`Received termination signal`时间戳（重启）；`Code: NNN`直方图；`<Error>` logger直方图；启动警告块（内存、THP、overcommit、`max_map_count`、TaskStats）。
**健康基线：**一次启动，没有Fatal，少量Error，启动块没有警告。
**危险信号：**反复启动但没有终止信号（崩溃/OOM kill，HC-1.2、P-13/P-30），Fatal块（P-30），某条消息每分钟重复数百次（重试循环——P-04/P-11/P-41），Keeper客户端错误（P-40），`broken`/`checksum`（P-31）。
**陷阱：****用grep，绝不要从头读到尾**；`.err.log`是`.log`的子集；默认50 MiB上限保留*尾部*；轮转文件仅在`-logs-include-archives`时包含；时间戳是服务器本地时间。
**搭配文件：**`system.text_log`（相同内容的结构化视图）、`system.crash_log`、`host_info`。

### keeper/（默认在onprem和gov；cloud仅在`-keeper-mntr on`时；v0.7+ 的bundle）
**为什么要采集：**bundle中其他所有Keeper信号都是*客户端*侧——本服务器等了多久（`metric_log`的`ZooKeeperWaitMicroseconds`）、它位于哪个成员（`zookeeper_connection`）、什么失败了（`zookeeper_log_errors`、999突发）。只有Keeper能说明这个成员是leader还是follower、它排队了多少请求、它自己的延迟、树有多大、运行哪个版本；没有这些，skill只能手动索取`mntr`输出。
**它回答的问题：**Keeper本身是否慢或过载，本服务器是否连在滞后的follower上，ensemble是否完整，Keeper版本是否老旧？
**先读：**每个成员的`Mode:` / `zk_server_state`（恰好一个leader；`standalone` = 单成员，无quorum）；`zk_outstanding_requests`（Keeper内部排队的请求——HC-3.10延迟的服务器侧对应物）；`Latency min/avg/max` / `zk_avg_latency`（自成员启动以来的毫秒数——生命周期平均值，因此`max`高而`avg`低代表过去的尖峰）；leader上的`zk_followers`对比`zk_synced_followers`（未同步的follower无法一致地提供读）；`zk_znode_count`、`zk_watch_count`、`zk_ephemerals_count`（树大小——数千万znode是快照和内存压力开始的点；与`system.tables` × 副本数对比）；`zk_approximate_data_size`、`zk_latest_snapshot_size`；`zk_version`（Keeper构建——升级建议针对它，而不是`system.version`）。自Raft/设置命令起：每成员`lgif`（`first_log_idx`、`last_log_idx`、`last_committed_log_idx`对比`leader_committed_log_idx`、`last_snapshot_idx`——日志位置，HC-3.19）；`conf`（`snapshot_transfer_chunk_size`、`write_snapshot_version`、`snapshot_distance`、`reserved_log_items`——生效的设置，HC-3.20）；`dirs`（快照和日志目录中的字节数）；`isro`（`ro` = 只读成员或observer）。
**健康基线：**`imok`；一个leader且所有follower已同步；`zk_outstanding_requests` 0到几十；`zk_avg_latency`个位数毫秒；文件数 = `configuration/`中的`<zookeeper><node>`条目数（工具远程运行或cloud中时只有已连接的那个成员——此时无法从这个目录判断“ensemble完整”）。
**危险信号：**`zk_outstanding_requests`达数百或更多（Keeper是瓶颈——与HC-3.8/3.10和P-40搭配）；`zk_synced_followers < zk_followers`；各文件中出现两个leader或没有leader（选举进行中 / 脑裂，P-57）；生产环境中`Mode: standalone`（无quorum，HC-3.7）；`zk_max_latency`以秒计；对`zookeeper_connection`认为已连接的成员出现`refused` / `timeout`（客户端端口有防火墙，或成员已宕机）；`zk_version`远落后于服务器（应点明的补救措施是升级Keeper）。成员返回`This instance is not currently serving requests`（HC-3.18）；某成员的`last_log_idx`低于leader的`first_log_idx`（需要完整快照，HC-3.19）；快照超过2 GiB且`snapshot_transfer_chunk_size=0`（HC-3.20、P-61）。
**陷阱：**计数器是**自成员启动以来**的，不是窗口值——生命周期`zk_avg_latency`为1 ms不能说明昨晚的情况；`system.zookeeper_connection`只指出每个连接*位于*哪个成员，因此只有工具能读取服务器配置时才会探测ensemble的其余成员（`execution_log.txt`会写`N connected + M more from the server configuration`）——某成员的文件缺失表示“本次运行无法发现”，不是“已宕机”；`timeout`文件仍可能包含被截断的回复（已如此标记）——其计数器应按不完整读取；`ruok`只能证明端口有应答；空回复意味着该词不在`four_letter_word_white_list`中；gov中主机是哈希——与`zookeeper_connection.host`连接，不要尝试解析。`srvr`、`mntr`、`conf`和`dirs`会在不提供服务的成员上拒绝，而`ruok`、`isro`和`lgif`仍会应答——因此在quorum丢失期间，`lgif`是显示哪个成员落后的那个；`skipped — … timed out`表示在成员第一次网络失败后命令未被发送，而不是命令失败；这些命令之前的bundle和Keeper不带`lgif`/`conf`/`dirs`（22.8上缺少`lgif`）——此时任何finding都不得假定某个设置；该目录混合了默认ensemble和每个**辅助ZooKeeper**（每个文件中的`# ensemble:`说明是哪一个），因此绝不要跨ensemble比较日志索引或设置；某节后面跟着`error: timeout … reply truncated`即为部分内容——不要读它的数字。
**搭配文件：**`system.zookeeper_connection`（本服务器位于哪个成员、会话年龄）、`metric_log_coordination_3_days`（每小时客户端侧延迟和异常）、`text_log_keeper_1_day`（会话标记）、HC-3.7–3.10、P-40、P-57。dashboard的Keeper部分把相同文件显示为**Keeper members**表，带HC-3.18–3.20标记，因此客户看到的就是预处理报告的内容。

### configuration/（有访问权限的onprem/cloud；gov中从不包含）
**为什么要采集：**自定义设置解释了偏离默认值的地方——内存限制、池大小、Keeper拓扑、存储策略、日志表TTL——以及只改了一侧的成对设置。
**它回答的问题：**服务器在默认值之外如何配置——内存限制、池、Keeper、存储、日志、用户限额？
**先读：**`config.d/*`和`users.d/*`文件名（定制了什么）；内存（`max_server_memory_usage*`、`max_memory_usage`、缓存）、后台池、`<zookeeper>`/`<keeper_server>`、`<storage_configuration>`、`<remote_servers>`、`<macros>`、`<interserver_http_host>`、`<*_log>`节（TTL？）、`<listen_host>`、profile约束/配额。
**健康基线：**少量有意为之、带注释的覆盖；日志表有TTL；Keeper quorum为奇数；显式interserver主机；开放的`listen_host`上没有无密码的`default`用户。
**危险信号：**成对设置只覆盖了一侧（P-16），池调高但CPU未变，`parts_to_throw_insert`达数万（从未改回的P-01保险），日志表没有TTL（P-17），偶数个/单个Keeper节点或重复macros（P-42），开放监听 + 无密码default（HC-10.10）。
**陷阱：**凭据已脱敏，但**主机名、IP、拓扑和标识符没有**——绝不可原样粘贴；无法解析的文件被跳过，因此缺失 ≠ 默认；评论前先确认该设置在此版本上存在（`clickhouse-source.md` §2）。
**搭配文件：**`host_info`（cgroup对比`max_server_memory_usage`）、`system.clusters`、`logs/`启动（被拒绝的配置）。

### dashboard.html（cloud/onprem）
**Keeper健康部分（新增）：**dashboard已从`DATA.keeper_metric_hourly`按小时应用Keeper健康测试（HC-3.8），并显示判定表、流量对比异常图（*unavailable*的柱子为红色，*blip*为琥珀色）、平均延迟线和每小时依赖Keeper的错误码。先读其判定，再用JSONL文件确认——dashboard在采集时实时查询了服务器，文件才是你可以引用的证据。
**为什么要采集：**它携带告警结果*以及匹配行*和少量预聚合面板；这是最快的headline（概要），也是记录已触发告警行的唯一地方。
**它回答的问题：**工具对相同数据的呈现，加上**带匹配行的告警结果**和一些JSONL中没有的面板（热门表列表、按用户汇总、主机检查）。
**先读：**提取`const DATA = {…}` JSON（配方见`reading-recipes.md` §1）；`alerts[]`（`rows`存在 = 已触发，`error` = 无法运行，`skipped` = 不适用）；`host_checks`；`version`/`uptime`；`query_slow`/`query_heavy`/`query_by_user`的预聚合视图；`high_part_count`；`ttl_activity`。
**健康基线：**没有已触发告警，主机检查全部`ok`/`info`。
**危险信号：**任何已触发告警（HC-1）——但要读规则的代码，而不是名称（2026年9月之前的工具版本中`too_many_simultaneous_queries`过滤的是252）；`uptime`很短（重启）。
**陷阱：**HTML从CDN加载Chart.js——对读取`DATA`无影响；告警`rows`含标识符；gov中不提供。其**Schema Graph**标签页点击时会把`schema_graph.html`（bundle-layout §8b）加载到frame中——那是独立文件，因此单独复制出的dashboard.html在该处显示空frame，不是bug。
**搭配文件：**仓库中的`alerts/*.yaml`（每条规则的阈值和SQL）。

### execution_log.txt（所有模式）
**为什么要采集：**结果文件无法说明哪些collector没运行、为什么、每个花了多少；文件缺失看起来和空表一模一样。
**它回答的问题：**每个collector是否都运行了；哪些失败（grant（授权）、缺表、超时）；哪些在此服务器上昂贵；每个阶段花了多久？
**先读：**头部——`node:`（这些system表描述的是哪个服务器；与案例相关的主机对比）、`uptime:`（采集前重启过的节点看起来比集群实际更健康）、`collection:`（当`-mode onprem`运行在SharedMergeTree集群上切换到cloud采集，或留在单节点及原因时存在）、`warnings:`（运行时打印的负载均衡器/混合主机行——当它削弱bundle时前缀为`Warning:`，当它是预期情况时前缀为`Note:`，cloud模式下的负载均衡端点就属于后者）；然后是*Summary*行；*Failed collectors*（错误文本——`Code: 60`未知表 = 此版本/配置上未启用，`Code: 497` = 缺少grant，`Code: 159` = 工具自己的`-query-timeout`触发，`Code: 139` = 没有Keeper/DDL配置）；*Most expensive collectors*。
**健康基线：**每个collector都`ok`，最慢的几秒，失败只出现在服务器没有的表上。
**危险信号：**某个collector报159（服务器对工具来说太慢——本身就是finding，HC-0）；`system.*`上497（bundle被grant收窄）；`part_log`、`query_log`或`zookeeper_log` collector花几分钟（日志表巨大——建议下次用更短的`-from/-to`）；某个阶段（dashboard、logs）主导运行时间。
**陷阱：**持续时间是从工具主机看的墙钟时间，包含网络；Native格式下`rows`为`-1`；更早的bundle没有此文件。
**搭配文件：**`HC-0`覆盖度、采集时间附近的`system.errors`错误码159/497、`running-the-tool.md` §8（重新采集）。

### alerts_summary.json（gov、`-skip-dashboard`或dashboard失败时）
**为什么要采集：**没有生成dashboard时，它是告警规则确实运行过以及哪些触发、报错或不适用的唯一证明。
**它回答的问题：**哪些告警规则运行了、触发了、报错了或不适用——不含任何匹配行。
**先读：**`fired`/`errored`/`not_applicable`/`evaluated`；每条规则的`state`和`instance_count`；`error`/`skip_reason`文本。
**健康基线：**`fired = 0`，`errored = 0`。
**危险信号：**按`severity`的已触发规则；`errored`规则**不是finding**（此版本缺少列/grant），但意味着检查没有发生——请自己从JSONL重新计算（`health-checks.md`）。
**陷阱：**`evaluated`不含跳过和报错的规则；任何模式下都不包含匹配行。
**搭配文件：**每条规则背后的JSONL文件（`system.parts`、`system.disks`、`system.replicas`、`query_log_details`、`system.mutations`、`system.detached_parts`、`system.crash_log`）。

---

## 场景走查——为什么这些文件要一起读

以上条目都是按文件组织的。真实事故会横跨多个文件；这些走查展示打开它们的顺序、要计算的数字、阈值，以及越过阈值意味着什么。标有 *（经验值）*的阈值来自支持经验，其余来自工具的告警或ClickHouse默认值。展示实时服务器查询是为了提供上下文；**bundle**列说明相同信号在归档中的位置。

### 重负载下的复制集群

触发条件：复制环境上的插入变慢或失败，`system.errors`中出现`TABLE_IS_READ_ONLY`/`KEEPER_EXCEPTION`，或`absolute_delay`攀升。

| 步骤 | 实时查询（仅供参考） | bundle中的位置 | 阈值 → 含义 |
|---|---|---|---|
| 1. 是否有副本落后，以何种方式？ | `SELECT type, count() FROM system.replication_queue GROUP BY type` | `system.replication_queue_*.jsonl` → 按`type`的`count()` | 任何单一类型**> 60**个条目 *（经验值）*→ 某副本正在变得不可用；`GET_PART`占主导 = 受fetch限制（网络/fetch池），`MERGE_PARTS`占主导 = 受merge限制（CPU/merge池）。读`postpone_reason`和`last_exception`找具体阻塞点（P-41）。 |
| 2. 服务器本身是否过载？ | `SELECT metric, value FROM system.metrics WHERE metric LIKE '%Pool%'` | `system.metric_log_7_days_*.jsonl` → 每小时`max_merge_pool_tasks`（= `BackgroundMergesAndMutationsPoolTask`）、`avg_/max_fetch_pool_tasks`（= `BackgroundFetchesPoolTask`）、`avg_/max_schedule_pool_tasks`（= `BackgroundSchedulePoolTask`）、`avg_/max_common_pool_tasks`；大小在`system.server_settings` / `system.settings` | 任何池值**> 256**，或池连续数小时被钉在其大小 → 系统过载（HC-2.7、HC-2.13/2.14、P-01、P-59）。v0.7之前的bundle只有merge池最大值和fetch池平均值——如需要请索取`system.metrics`。 |
| 3. Keeper是否已经放弃该副本？ | `SELECT database, table, replica_name FROM clusterAllReplicas(default, system.replicas) WHERE is_readonly` | `system.replicas_2*.jsonl` → `is_readonly`、`is_session_expired`；`system.errors` 999/242；`metric_log.zk_hw_exceptions` | 任何`is_readonly = 1` → 在会话重新建立前，对该表的写入会被拒绝；同一小时出现`is_session_expired = 1`或`zk_hw_exceptions > 0` → 原因是Keeper超时/会话过期（P-40），不是表本身。 |
| 4. 负载由什么构成？ | — | `system.part_log_3_days`每小时NewPart和`size_in_bytes/count`；`query_log_details`的Insert `count`和`written_rows/count` | 每小时数千次微小插入 → Keeper事务率和part数量是自找的（P-02）；否则看是否有merge/mutation阻塞池（P-04、P-11）。 |

解读：第1步告诉你*哪一侧*落后，第2步是否容量耗尽，第3步协调是否中断，第4步要改什么。把四个数字放在一起呈现——队列高、池空闲且没有Keeper错误，是merge被阻塞，而不是服务器过载。

### Keeper变慢、过载或增长——找出谁、何时、何处（≥ 26.3）

1. `system.aggregated_zookeeper_log_by_minute_1_day` → 分钟粒度：加权延迟、请求突发、`loss_failures`；cloud中是否有某个副本突出（HC-3.21——只有一个时为P-65）。
2. `system.aggregated_zookeeper_log_by_hour_2_days` → 谁：组件占比及其小时形态（P-68热循环）、慢操作（P-66、P-67）、返回`ZNONODE`的remove（P-62）。
3. `system.aggregated_zookeeper_log_paths_1_day` → 何处：请求所在的类别（`in_multi` → P-64）和净创建所在的类别（P-63）。
4. `keeper/<member>.txt` → `zk_znode_count`、`zk_approximate_data_size`、`zk_outstanding_requests`、快照大小——增长或负载对ensemble造成什么（HC-3.18–3.20、P-61）。
5. `part_log_3_days`、`system.parts` / `system.columns`、`asynchronous_insert_log_7_days`、`system.settings`（`compatibility`、去重和异步插入设置）→ 路径背后的原因（每次插入多少part、每个part多少文件、flush大小）。
6. 低于26.3时这三个文件不存在：回退到`metric_log_coordination_3_days`、`metric_log_by_host_3_days`、`text_log_keeper_1_day`和`zookeeper_log_errors_1_day`（若启用），如果客户的服务器 ≥ 25.9，就把`reading-recipes.md` §Keeper requests中的实时查询给他们。

### 插入被TOO_MANY_PARTS (252) 拒绝

1. `system.parts`（active）→ 每个`(database, table, partition_id)`的part数；**> 300**= 告警，≥ `parts_to_throw_insert`（≥ 23.6上OSS默认3000）= 正在拒绝（HC-2.1）。
2. 同一文件 → `countIf(level = 0)`和`avg(rows)`；数百个level-0 part且平均行数**< 10 000**→ 插入太小（P-02）。
3. `system.tables` → 该表的`partition_key`；按小时/按租户的键 → 扇出（P-03）。
4. `system.part_log_3_days` → 每小时`NewPart`对比`MergeParts`，以及`error != 0`的`MergeParts`（241 = merge OOM循环，P-11）。
5. `system.merges` → `elapsed`以小时计且`progress`停滞的merge占用一个槽位（P-04）。
6. `system.mutations` → 该表上数百条`DELETE`/`UPDATE`命令（P-05）。

### 查询以MEMORY_LIMIT_EXCEEDED (241) 失败

1. `system.query_log_details_7_days` → `exception`文本类别：*(for query)* / *(total)* / *(for user)* / *while pushing to view* / *AggregatingTransform* / *FillingRightJoinSide*（P-10）。
2. `system.metric_log_7_days` → 故障小时的`avg_memory_tracking_bytes`对比RAM（`host_info.memory.total_bytes`、cgroup）→ 服务器到顶（P-12）还是某条查询（P-52）。
3. `system.part_log_3_days` → 同一小时merge的`peak_memory_usage`；`system.*`表居首 → P-17。
4. `configuration/` → `max_server_memory_usage*`、`max_memory_usage`、缓存大小；`host_info.clickhouse_relevant_tunables` → THP/overcommit（P-13）。

### 磁盘被填满

1. `system.disks` → `free_pct`**< 15**critical，**< 5**插入失败（HC-4.1）。
2. `system.parts`（active）→ 按`database`的`sum(bytes_on_disk)`；`system`进入前三 → 日志表没有TTL（P-17）。
3. `system.detached_parts` → `sum(bytes_on_disk)`；`logs/`大小；日志卷的`host_info.disks`。
4. `system.tables` DDL有`TTL`但`part_log`中没有TTL事件 → TTL未生效（P-18）。

### 升级后变慢或性能回退

1. `system.version` + `logs/`启动banner → 版本何时变化；`system.clusters`/banner中版本混杂（P-31）。
2. `system.query_log_details_7_days` → 变化小时前后每个hash的`query_duration_ms/count`和`memory_usage/count`（P-54）。
3. `system.part_log_3_days` → 变化前后每小时merge持续时间/大小。
4. 两个时期都采集了的话看`query_analysis/profile_events_compare`；否则对最差的hash建议`--normalized-query-hash`。
5. `clickhouse-source.md` → 两个构建之间的release notes。

### 服务器崩溃或重启

1. `system.crash_log` → 带`signal`、`version`、`query_id`、顶部帧的行（P-30）；空文件 + `logs/`中重启 → 被OS OOM kill（P-13）。
2. `logs/clickhouse-server.log` → `Starting ClickHouse`/`Received termination signal`时间线；`<Fatal>`块；启动警告块。
3. `host_info.json` → `memory.available_bytes`、cgroup限制、THP、overcommit。
4. `system.query_log_details_7_days` → `crash_log.query_id`对应模式的hash；`system.errors` 49/1001计数。

### 表位于对象存储的集群上的Keeper故障

1. `system.metric_log_coordination_3_days` → `sum(ProfileEvent_ZooKeeperHardwareExceptions)`爆炸而`…ZooKeeperTransactions`崩塌的小时（HC-3.8）。这些小时就是事故；其他都是之前或之后。
2. `system.part_log_3_days` → 那些小时里`error = 999`的`MergeParts`，之后`NewPart > 0`而`MergeParts = 0`的小时（HC-2.12）：merge停止了。`system.query_log_details_7_days` → 插入上的999/319，DDL上的571和57（`.tmp.inner_id` UUID冲突），然后最繁忙插入目标上的252（P-57）。
3. `system.zookeeper_connection` → 恢复以来的会话年龄；`system.zookeeper_log_errors_1_day`（若存在）→ 每小时会话数和`ZSESSIONEXPIRED`；`configuration/zookeeper.xml` → ensemble大小；`system.databases` → `Replicated`数量（负载乘数）。
4. `system.distributed_ddl_queue` → 未`Finished`的条目、被重放的`CREATE OR REPLACE`语句（HC-3.11）。`system.errors` → 221/86计数对比`Uptime`（恢复噪声还是持续存在）。
5. **恢复后：**`query_log_details` → 按表统计`Active` part上的107 `FILE_DOESNT_EXIST` / `The specified key does not exist`；`system.disks.type = ObjectStorage` + `storage_policies`确认为对象存储；每副本`system.metrics` → `MetadataFromKeeperCacheObjects`偏斜；`blob_storage_log_7_days` → 该小时的删除/失败上传（P-58）。在做任何涉及数据的操作之前，建议刷新元数据（`SYSTEM RESTART REPLICA`、`SYSTEM DROP DISK METADATA CACHE`、滚动重启）。
6. **覆盖度注意事项：**在SharedMergeTree集群上，`onprem` bundle是N个副本中的一个（HC-0）——要说明是哪一个，并建议用`-mode cloud`获取集群视图。Keeper自己的`mntr` / `srvr`输出从v0.7起在`keeper/`中（onprem和gov；cloud仅在`-keeper-mntr on`时）——先读它再索取；Keeper自己的日志文件仍未采集。
