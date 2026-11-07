# 它采集什么以及 bundle（诊断包）

*已从主 [README](../README.md) 迁移而来，该文件保留每一节的简短版本。*

## 采集什么以及为什么

| 采集内容 | 它回答的问题 | 为什么在 bundle（诊断包）中 |
|---|---|---|
| `system.version` | 这是哪个构建？ | 每个默认值、限制和 bug 修复都与版本相关。 |
| `system.parts`（活跃，最大 50 000 个） | 有多少 part（数据分片）、多大、碎片化程度如何、位于哪块磁盘？ | 每个分区的 part 数量是写入/merge（合并）问题的最早信号（`TOO_MANY_PARTS`）。 |
| `system.part_log_3_days`（3 天，`system.part_log` 的按小时聚合） | merge 跟得上写入吗；是否有 merge 或 mutation（变更）失败？ | 展示写入大小、merge 吞吐量以及失败的后台操作及其错误码。 |
| `system.merges`, `system.mutations` | 当前正在 merge 或执行 mutation 的是什么；什么卡住了？ | 卡住的 merge 或 mutation 积压，通常是 part 越堆越多而线程池看起来空闲的原因。 |
| `system.replicas`, `system.replication_queue` | 每个 replica（副本）都可写且已追平吗；如果没有，为什么？ | 只读状态、Keeper 会话丢失以及队列形态可以定位复制问题。 |
| `system.replicas_problem_tables`, `system.database_replicas` (≥ 25.9), `system.replicas_answered` (cloud) | 对于出问题的表和 Replicated 数据库：所有副本是否存活、落后日志多远、Keeper 拒绝了什么；哪些副本做出了应答 | 由 Keeper 支撑的列只在真正需要的地方读取（最多 200 张表，优先问题表）：如果向 `system.replicas` 或 `system.clusters` 索取这些列，就会从 Keeper 读取每张表或每个数据库的状态，在拥有数万张表的服务器上这需要数分钟。 |
| `system.query_log_details_7_days`（`system.query_log` 的按小时聚合） | 运行了什么、有多慢、用了多少内存、什么失败了、由谁执行？ | 大多数事故都始于工作负载；这是聚合视图，每个查询模式保留 500 字符的样本，不包含客户数据行。 |
| `system.query_log_by_hour_7_days` / `system.query_log_by_pattern_7_days` (onprem, cloud) | 每小时运行了多少查询、属于哪种类型、有多慢；哪些模式最慢、最重，以及属于谁？ | 每个查询只计一次——`query_log_details_7_days` 会按表重复一个查询——也是 dashboard（仪表盘）查询面板读取的内容。模式文件将哈希保留为文本，并保留一个 600 字符的 SQL 样本。 |
| `system.parts_by_table` (onprem, cloud) | 每张用户表持有多少 part、行和字节？ | 每张表一行且不设上限——`system.parts` 只保留最大的 50 000 个 part——供 dashboard 的摘要、存储、最大表和表浏览面板使用。 |
| `system.query_log_exceptions_1_day`（按小时，仅失败查询） | 每小时有多少查询因各个错误码失败？ | 异常 alert rule（告警规则）读取的精确计数——`query_log_details_7_days` 会按查询涉及的每张表重复一次——gov 下不包含异常文本。 |
| `system.query_views_log_3_days`（3 天，`system.query_views_log` 的按小时聚合） | 每个 materialized view（物化视图）都触发了吗、是否有失败、各自的成本是多少？ | 一个“成功”但没有写入任何内容的 MV 在 `query_log` 中是不可见的。抛异常的 MV 默认会使父 INSERT 失败（`query_log` 显示 `… while pushing to view X`），但在 `materialized_views_ignore_errors = 1` 时 INSERT 是干净的，而这里是唯一的记录。无论哪种情况，基础 part 都已经提交。 |
| `system.view_refreshes` (≥ 23.12) | 可刷新的 materialized view 真的在刷新吗？ | `REFRESH EVERY` 视图从不出现在 `query_views_log` 中；这里是它的调度、上次成功和上次错误唯一所在之处。 |
| `system.data_skipping_indices` | 哪些表带有数据跳过索引、基于哪些表达式、粒度如何？ | 为“索引存在但查询没有使用它”提供证据；也会绘制在 schema graph（模式关系图）上。 |
| `system.errors`, `system.text_log`（24 小时，按严重级别优先，每个 logger ≤ 200 行） | 哪些错误、出现频率如何、消息是什么？ | 按错误码快速分诊；日志切片给出服务器自己的说法。 |
| `system.text_log_histogram_1_day` | 每小时 Warning 及更严重的日志行，按级别和组件划分，每类各一个示例 | 说明错误*何时*开始以及*哪个*组件，不受 `text_log` 2000 行上限的影响。 |
| `system.error_log_7_days` (≥ 24.8) | 服务器中任何位置抛出的每个错误码，按小时统计，包含后台线程 | `system.errors` 背后的历史：即使没有查询失败，也能把 999 / 242 / 252 / 107 放到时间线上。 |
| `system.text_log_keeper_1_day` | 每小时 Keeper 会话标记（过期、结束、重连、连接到哪台主机） | Keeper 会话丢失的那一分钟以及服务器重连到哪里——这些行低于 Warning 级别，永远不会进入 `text_log`。 |
| `system.aggregated_zookeeper_log_by_hour_2_days`, `_by_minute_1_day`, `_paths_1_day` (≥ 26.3) | 哪个 ClickHouse 组件发送哪些 Keeper 请求以及有多慢（按小时）；每分钟的 Keeper 延迟、量和失败（cloud 下按副本）；请求最多和净创建最多的 Keeper 路径 | 说明**谁**在给 Keeper 施加负载、**何时**变慢以及 znode 在**哪里**堆积——来自一张默认开启的表（自 25.9 起，保留 30 天），而 `zookeeper_log` 通常并非如此。 |
| `system.metric_log_7_days`（`system.metric_log` 的按小时聚合） | 内存和后台线程池负载随时间的变化 | 区分“服务器过载”和“某个查询行为异常”。 |
| `system.disks`, `system.detached_parts` | 磁盘快满了吗；是否有数据因损坏被搁置？ | 磁盘写满能解释许多其他症状；detached part（分离 part）记录了损坏或复制残留。 |
| `system.tables`, `system.columns`, `system.dictionaries`, `system.clusters` | schema、键、materialized view（物化视图）、dictionary、拓扑 | part 和查询中的 finding（发现）由 schema 和集群定义来*解释*。 |
| `system.settings`, `system.server_settings` (≥ 23.3), `system.merge_tree_settings` | 哪些查询/profile、服务器和全服务器 MergeTree 设置偏离了默认值——第三层包含 `TOO_MANY_PARTS` 阈值，在 SharedMergeTree 上还包含每表 fetch 批大小和 leader 选举周期 | 无需配置副本即可回答“调过什么”——cloud 的 bundle 没有 `configuration/`；在 gov 下，可识别服务器身份的值会被 `REMOVED`。 |
| `system.asynchronous_insert_log` (7 天) | 异步写入的 flush 成功吗、有多慢？ | 当 `wait_for_async_insert = 0` 时，丢失的 flush 是无声的。 |
| `system.crash_log`, `system.stack_trace` | 服务器崩溃了吗；它的线程当时在做什么？ | 崩溃证据需要堆栈跟踪以及触发它的查询。 |
| `system.metrics`, `system.events`, `system.asynchronous_metrics` | 实时 gauge 和累计计数器：Keeper 会话和 watch、只读副本、进行中的 fetch、对象存储请求、缓存大小、`Uptime` | 按小时聚合无法提供的“此刻”状态；`Uptime` 将 `system.errors` 和 `system.events` 的计数转换为速率。 |
| `system.metric_log_coordination_3_days`（3 天，按小时，列由正则选取） | Keeper、对象存储、文件系统缓存和复制计数器逐小时变化 | 即使没有查询失败，凌晨 03:00 的 Keeper 故障或 S3 错误突发也能在这里看到。 |
| `system.metric_log_by_host_3_days`（仅 cloud；3 天，按小时，按副本） | 哪个副本的后台线程池被打满、实时线程池大小是多少、Keeper 延迟和 CPU 如何；在 SharedMergeTree 上，每小时被选中 fetch 的 part 与已开始的 fetch 以及 leader 选举 | `metric_log_7_days` 会把各副本折叠在一起；这是用于定位 parts-propagation-lag 或 Keeper 延迟 finding（发现）的按主机视图，并能显示线程池大小的变化如何逐台主机显现。 |
| `system.zookeeper_connection` (≥ 23.8；`xid` 自 24.3), `system.databases`, `system.storage_policies` | 哪个 Keeper 节点、会话有多旧、请求计数器已经走到多远；有多少个 `Replicated` 数据库；哪些磁盘支撑哪个策略 | 复制和“文件不存在”类 finding（发现）背后的协调与存储拓扑。 |
| `system.parts_max_block_recent`（所有模式）, `system.parts_max_block_by_replica` (cloud) | 哪个副本在哪个分区上落后、落后多少个 block，以及哪些副本完全不含某个分区 | 在 SharedMergeTree 上，落后的副本返回的行数比同类副本少。block 编号按分区分配且单调递增，因此副本最新 block 与集群最新 block 之间的差距，就是它尚未 fetch 的 part 的真实数量——而某个副本完全不含某分区的 part 时不会贡献可比较的行，这种情况通过从表的副本集合中减去该分区的副本集合来发现。完整的 `system.parts` 转储只在一个副本上读取，并保留最大的 50 000 个 part，这恰好会丢掉那些能显示落后的新鲜小 part。 |
| `system.distributed_ddl_queue` (7 天), `system.replicated_fetches` | 卡住或失败的 `ON CLUSTER` / Replicated 数据库 DDL 及其按主机状态；进行中的 part fetch | DDL 重放风暴（`.tmp.inner_id` 表上的 `TABLE_ALREADY_EXISTS`，错误码 571）和卡住的 fetch 只能在这里看到。 |
| `system.zookeeper_log_errors_1_day`, `system.blob_storage_log_7_days`（仅当这些表启用时） | 每小时失败的 Keeper 请求、操作和错误码（仅错误——该表太大，无法整体聚合）；每小时的对象存储上传、删除和失败 | 为“Keeper 停止应答”和“blob 被删除 / 从未写入”提供直接证据。 |
| `host_info.json` (onprem) | 操作系统、CPU、RAM、磁盘、THP、overcommit、限制、cgroup | 相当大一部分 self-managed 事故是 ClickHouse 自己在启动时就会警告的主机设置。 |
| `logs/` (onprem) | 重启、启动警告、致命堆栈、事故的第一个错误 | 系统表在重启后会丢失这些信息；日志文件则保留它们。 |
| `keeper/<host>_<port>.txt`（onprem，gov 下主机名哈希） | 每个 Keeper 成员对自身的描述：leader 还是 follower、Raft 日志位置、快照大小和传输设置、未完成请求、自身延迟、znode 数量、版本 | Keeper 延迟或会话丢失的 finding（发现）要在 Keeper 侧定论；没有任何 `system.*` 表携带这些信息。 |
| `configuration/` | 哪些设置偏离默认值 | 内存限制、线程池、Keeper、存储策略、日志表 TTL——已移除凭据。 |
| Alert rule（告警规则）结果 + `dashboard.html` | 哪些已经超过阈值 | 19 条只读规则在任何人打开文件之前就给出 headline（概要）。 |

每个文件的细节（列、窗口、gov 差异）见 [模式与查询布局](modes.md#模式与查询布局)；对于 bundle（诊断包）的读者，另见 `skills/clickhouse-diagnostic/references/file-guide.md`。

## 阅读 bundle（诊断包）

三种方式，深度递增：

1. **`dashboard.html`**——从结果文件夹中打开它；alert rule（告警规则）、存储、查询活动、复制、磁盘、主机可调参数以及（在请求时）查询分析，全都集中在一个自包含页面中。参见 [Dashboard（仪表盘）](dashboard.md#dashboard仪表盘)。
2. **`inspect_bundle.py`**——一个仅使用标准库的 Python 预处理脚本，可从归档或文件夹中在终端打印覆盖范围、清单和确定性健康检查：
   ```bash
   python3 skills/clickhouse-diagnostic/scripts/inspect_bundle.py clickhouse_diagnostic_<ts>.tar.gz
   ```
3. **AI skill**——`skills/clickhouse-diagnostic/` 教会 Claude Code、Codex CLI 或任何读取 `SKILL.md`/`AGENTS.md` 的助手如何阅读 bundle（诊断包）以及如何运行工具：每个文件回答什么、与 alert rule（告警规则）对齐的阈值、常见 ClickHouse 故障模式的匿名化目录、带证据（`file → column → value`）的摘要模板、建议操作和后续提示，以及当 bundle 未覆盖该问题时的确切重新采集命令。在 Claude Code 中，打开此仓库时会自动发现该 skill（`.claude/skills/clickhouse-diagnostic`，一个符号链接）——运行 `/clickhouse-diagnostic <bundle>` 或直接说“分析这个 bundle”；Codex 则通过根目录的 `AGENTS.md` 指向它（`.agents/skills/clickhouse-diagnostic`）。要从其他目录使用它，请把 `skills/clickhouse-diagnostic` 复制或符号链接到助手的 skills 文件夹。该 skill 绝不会修改或上传 bundle；参见 `skills/clickhouse-diagnostic/references/privacy.md`。