# Dashboard（仪表盘）与 schema graph（模式关系图）

*已从主 [README](../README.md) 迁移而来，该文件保留每一节的简短版本。*

## Dashboard（仪表盘）

未设置 `-skip-dashboard` 时，工具会在每次运行的结果文件夹中生成一个自包含的 `dashboard.html`。dashboard（仪表盘）将所有查询结果以内联 JSON 的形式嵌入，只从 CDN 加载 Chart.js，因此可以在任何有互联网访问的机器上从磁盘（`file://`）打开。

每个面板都由 collector（采集器）已经写出的文件构建（`internal/dashboard/fromfiles.go`）：**采集完成后不会再向服务器发送任何查询**，因此 dashboard 不会给已经吃力的服务器增加任何成本，并且会准确展示实际采集的窗口——包括 `-from`/`-to` 以及 essential 运行的 1 天上限。如果某个面板的来源 collector 被跳过或失败，其名称会连同 collector 名称一起列在横幅中；如果某个面板所需的表在此服务器上不存在，则该面板会像以前一样被隐藏。使用 `-output-format native` 或 `tsv` 时，面板会像以前一样改为向服务器查询。与实时面板的差异：Part Log 面板覆盖 collector 的 **3 天**，而不是 7 天；cloud bundle（诊断包）的 Replicas Health 显示正在服务的副本中受影响最大的 200 张表（`system.replicas` collector 每个副本保留最差的 200 张）；schema graph 的 `CREATE` 语句是服务器原始的单行文本（只有实时的 `formatQueryOrNull` 才能美化它们）。

### 分区

| # | 分区 | 展示内容 |
|---|---|---|
| 1 | 🚨 **Alert Summary（告警摘要）** | 已触发的 alert rule（告警规则）按严重级别（critical / warning / info）分组，行级消息模板按实例展开。长内容会折叠：消息只展示到 ClickHouse 堆栈跟踪之前的部分（真实堆栈有 15 行、1700+ 字符，其中约 200 字符是错误信息），并限制在 260 字符以内，完整文本可点击 *完整消息和堆栈跟踪* 查看；只列出前 5 个实例，其余收在 *N 个实例* 之后；规则描述只显示第一段，其余收在 *关于此规则的更多信息* 之下。页面不会丢弃任何内容——`DATA.alerts` 仍完整携带每一行。**无法运行**的规则会出现 ⚠ 标记和一个单独的 “无法运行” 标签——它们绝不会计入严重级别徽章。此处**不适用**的规则会列在颜色较淡的脚注中。没有任何触发时会出现绿色的 “未发现问题” 横幅。 |
| 2 | 📈 **Overview（概览）** | 顶级计数器：服务器版本、运行时间、数据库总数、表总数、活跃 part、总大小 |
| 3 | 📦 **Storage（存储）** | 按数据库的大小（水平条形图）、表引擎分布（环形图），以及按大小排序的前 20 张表列表 |
| 4 | 📋 **Tables Explorer（表浏览）** | 可搜索 / 可分页的所有用户表表格，包含引擎、part、行、大小、分区 / 排序键和存储策略 |
| 5 | 📊 **Query Activity（查询活动）**（最近 7 天） | 按类型统计每小时查询数（堆叠条形图）、按类型计数、按类型的平均耗时和内存 |
| 6 | 🔍 **Query Deep Dive（查询深入分析）**（最近 7 天） | 最慢的 20 个查询模式（按平均耗时）、读取最重的 20 个查询（平均 MB/查询）、按用户的执行次数和错误、慢查询详情以及按用户汇总表 |
| 7 | ⚠️ **Exceptions（异常）**（最近 7 天） | 按计数排名靠前的异常码，以及一张按错误码展示最近一条消息的异常详情表 |
| 8 | 🔧 **Part Log Events（part 日志事件）**（最近 3 天——collector 的窗口） | 按事件类型统计每天的 part 事件（堆叠条形图）以及事件类型分布 |
| 9 | 📖 **Dictionaries（字典）** | 状态分布（LOADED / FAILED / LOADING）、每个 dictionary 分配的字节数、生命周期配置（min/max），以及一张包含 `last_exception` 的详情表（gov 模式下此处输出空值） |
| 10 | 💥 **Crash Log（崩溃日志）** | 来自 `system.crash_log` 的最近条目——表为空时该分区隐藏 |
| 11 | ⏳ **Pending Operations（待处理操作）** | 待处理的 mutation 和 detached part 表并排展示 |
| 12 | 🔄 **Replication Queue（复制队列）** | `system.replication_queue` 中的当前条目，包含类型、表和最后一次异常 |
| 13 | 🌐 **Cluster Nodes（集群节点）**（仅 cloud 模式） | `default` 集群中的主机，以及 shard / replica / 活跃状态 |
| 14 | 🔁 **Replicas Health（副本健康）** | 复制延迟分布、按表统计的队列大小、按副本的详情（仅当存在复制表时显示） |
| 14a | 🔑 **Keeper Health（Keeper 健康）**（最近 7 天） | 当 `system.metric_log` 有行时显示。按小时展示：Keeper 事务（折线）与硬件异常（按判定结果着色的柱状图）、平均请求延迟（`ZooKeeperWaitMicroseconds / ZooKeeperTransactions`）、依赖 Keeper 的错误码（24.8+ 上来自 `system.error_log` 的 999 / 242 / 319 / 571 / 252，否则来自 `query_log`），以及实时的 `system.zookeeper_connection` 行。判定表应用与 `keeper_health` alert rule（告警规则）相同的规则：异常超过 1000 且流量低于 7 天中位数的 50% = 不可用；流量保持 = 短暂抖动。只要 bundle（诊断包）中包含 `keeper/`，就会显示 **Keeper 成员** 表（即使没有 `metric_log`），列出每个成员对自身的报告——ensemble、状态、角色、版本、Raft 日志 first → last 与 committed 对 leader-committed、最新快照、`snapshot_transfer_chunk_size`、快照格式、磁盘字节数——并按照与预处理脚本的 HC-3.18–3.20 相同的规则标记 ⛔ / ⚠（有一项测试会对相同文件运行两个读取器并要求判定一致）；当*该 bundle 中*某个 ensemble 的所有成员都未在服务时显示横幅——这并不排除该 ensemble 之外、仍持有 quorum 的投票成员。Apache ZooKeeper 的 “This ZooKeeper instance is not currently serving requests” 与 Keeper 的措辞一样计入。 |
| 15 | 💾 **Disk Usage（磁盘使用）** | 每块磁盘的空闲与已用空间，以及一张磁盘详情表 |
| 16 | 🛑 **Server Error Counters（服务端错误计数器）** | 来自 `system.errors` 的前 20 个累计错误码、高 part 数分区（>100 个 part → 潜在的 code-252 `TOO_MANY_PARTS` 风险），以及来自 `part_log` 的 TTL 活动 |
| 17 | ⚡ **Async Insert Activity（异步写入活动）**（最近 24 小时） | 按状态统计每小时 flush 次数——当 `system.asynchronous_insert_log` 为空或处于 gov 模式时该分区隐藏 |
| 18 | 🕸 **Schema Graph（模式关系图）** | 表依赖关系图——表、materialized view（物化视图）、dictionary 和 Distributed 表，以及数据流经的边——由第二个页面 `schema_graph.html` 提供，标签页只在点击时才将其加载进 frame。参见下文 *Schema graph*。 |

此外，当设置了 `--query-id` 或 `--normalized-query-hash` 时，导航栏顶部附近会出现一个 **🔍 Query Analysis（查询分析）** 分区。其内容参见 [Query analysis mode（查询分析模式）](query-analysis.md#查询分析模式)。

左侧边栏列出所有分区，并高亮你正在阅读的那一个。粘性页头左侧的箭头可隐藏或显示边栏（与主题一样，选择会记录在浏览器中）；隐藏时，箭头会显示当前分区的名称。依赖集群特定或版本特定数据的分区（Crash Log、Cluster Nodes、Replicas Health、Async Inserts、Query Analysis）在没有内容可显示时会被隐藏。

### 在没有故障的情况下预览 Keeper Health 面板

`make dashboard-preview` 会从匿名化 fixture（固定样例）渲染三个页面。`bin/alerts_preview.html` 覆盖 Alert Summary 的每一种折叠情况——带堆栈跟踪的消息、实例数超过内联上限、无需展开的短消息、查询失败的规则、不适用的规则。`bin/keeper_incident_preview.html` 的形状类似共享存储集群上一次真实的 Keeper 故障：48 小时的 Keeper 计数器（第一天短暂抖动，第二天 quorum 丢失八小时）、`keeper_health`、`keeper_connection_blips`、`merges_stalled`、`background_operation_failures`、`high_exception_rate` 和 `too_many_parts` 这些 alert rule 按它们会触发的样子展示、每小时的错误码，以及重新建立的 Keeper 会话。在事故发生前用它查看面板和 alert rule 的样子，或用来审查主题或措辞改动。`bin/schema_graph_preview.html` 是一个小型 fixture 管道，包含所有节点类型——MergeTree 源表、带显式目标和隐式目标的 MV、可刷新 MV、dictionary、Distributed 表、view，以及一张 DDL 中含凭据的表——可从预览 dashboard 的 Schema 标签页访问。

### 哪些是交互式，哪些是静态的

- **交互式**：Tables Explorer（全文搜索、database/engine 过滤、分页）；所有图表（悬停提示、图例切换）；Alert Summary 的展开项（原生 `<details>`，因此无需 JavaScript 也能工作，但只有展开时才能被 `Ctrl-F` 找到）。
- **静态**：其他所有表——它们按固定顺序渲染，但底层 JSON 已嵌入页面，因此如果想要原始值，可以运行 `grep DATA dashboard.html | head`。


### schema graph 页面

页面的外观以及每个元素的含义见上文 [Schema graph（模式关系图）](#schema-graph模式关系图)；本节是设计记录。

`schema_graph.html` 写在 `dashboard.html` 旁边，改编自 ClickHouse 自己的 `/schema` 页面（`programs/server/schema.html`，Apache-2.0），其中每个实时查询都替换为采集时嵌入的 JSON：`system.tables`、`system.columns`、`system.dictionaries` 和 `system.view_refreshes`，排除 system 数据库。节点按引擎着色（MergeTree、MV、可刷新 MV、dictionary、Distributed、view）；边遵循 `dependencies_*` / `loading_dependencies_*`，并在 26.6+ 上遵循 `target_table`——这是唯一能为使用 `ENGINE` 声明的 MV 命名其隐式 `.inner_id.*` 表的来源。点击节点可查看其键、列、邻居和 `CREATE` 语句；搜索会匹配表名和列名；支持拖动、缩放和按数据库过滤。

为什么要第二个文件：该图需要嵌入每张用户表的每一列，而数据量会随 schema 增长——在几乎为空的服务器上约 750 KiB，在拥有数千张表的服务上可达数十 MB。将其内联会使 `dashboard.html` 对每个读者都变慢。因此 dashboard 的 **Schema Graph** 标签页会在首次点击时把该文件指定为 `<iframe>` 源（一次导航，`file://` 允许这种操作，而 `fetch()` 同级文件会被阻止），这样在有人主动请求之前，没有人需要为图付出代价。两个页面通过 `localStorage` 共享主题；无法跨 `file://` 边界对 frame 执行脚本，也不需要这样做。如果 frame 一直空白，说明 bundle 文件夹没有被完整复制。

**凭据。** `create_table_query`、`engine_full` 和 `system.dictionaries.source` 携带引擎参数——S3 密钥、MySQL 密码、`kafka_sasl_password`。23.x 起的服务器会自行将这些掩码为 `'[HIDDEN]'`；22.x 服务器不会。采集到的 JSONL（`system.tables`、`system.dictionaries`）和图载荷都会经过一个 redactor（`internal/collection/sqlredact.go`），它会掩码所有已知引擎和表函数的凭据位置（S3 系列、MySQL、PostgreSQL、MongoDB、`remote()`、Redis、Azure……），并应用配置脱敏器所用的字节形态启发式（URL basic-auth、AWS key ID、JWT、`keyword = 'value'`），写入服务器自己的 `[HIDDEN]` 标记，使新旧 bundle 读起来一致。`execution_log.txt` 会记录每个 collector 替换了多少个值。这是字段级且仅限 JSONL 的：`-output-format native|tsv` 的 bundle 会携带未脱敏的 DDL，并会说明这一点。

## Schema graph（模式关系图）

![schema graph：一个数据库分区及其表、materialized view（物化视图）、dictionary 和 Distributed 表，边遵循数据流，每个节点列出列，键列以红色显示](images/schema-graph.png)

`cloud` 或 `onprem` 模式下的每次运行还会在 `dashboard.html` 旁边写入 **`schema_graph.html`**：一张交互式地图，展示数据如何在 schema 中流动，改编自 ClickHouse 自己的 `/schema` 页面，完全由采集时捕获的数据渲染——它可以从磁盘工作，不需要服务器，也不需要网络。

- **每张表一个节点**，按数据库分组并按引擎着色：MergeTree 系列、materialized view（物化视图）、可刷新 MV、dictionary、Distributed、view。每个节点列出其列（键列红色，带默认值的列绿色），对于持有数据的表，还显示行数和大小。
- **边遵循数据流。** materialized view 位于它读取的表和它写入的表之间——包括使用 `ENGINE` 声明的 MV 的隐式 `.inner_id.*` 表；dictionary 指向其源表；Distributed 表指向其背后的 shard 表。链路从左到右排列，最深的在前，因此最长的 MV 管道会出现在顶部。当 `system.tables` 没有记录依赖关系时——可刷新 MV、普通 view、Distributed 表——源边会**从 DDL 推断**（`FROM` / `JOIN`，或 Distributed 引擎参数）并画成**虚线**，侧边栏也会说明这一点。
- 节点下方的**灰色条带**承载了你原本需要打开 DDL 才能看到的信息：表的行数和大小；可刷新 MV 的调度——`EVERY 2 MINUTE OFFSET 30 SECOND RANDOMIZE FOR 10 SECOND`——当**没有** `RANDOMIZE FOR` 时，调度会带下划线，因此三十个在同一瞬间触发的 view 会读作三十条下划线；dictionary 的 `LIFETIME`；表的 data-skipping 索引数量。
- **列名会显示其角色**，而不是只有一种红色：主键粗体红色，仅排序键红色，分区键琥珀色点状下划线，采样键青色虚线，带默认值的列绿色——一列可以同时带有多种角色。侧边栏会为每一列标注 `PRIMARY KEY` / `ORDER BY` / `PARTITION BY` / `SAMPLE BY`，并列出跳过索引及其类型、表达式和粒度。
- **点击节点**可查看其键、分区、完整列列表、它读取和写入的表、可刷新 MV 的刷新调度和状态、dictionary 的生命周期、状态和最后一次错误，以及 `CREATE` 语句——服务器的原始文本（页面在采集后由已采集文件构建，只有实时的 `formatQueryOrNull` 才能美化它），并在页面上做语法高亮，引擎参数中的凭据显示为 `[HIDDEN]`。
- **搜索**会匹配表名*和*列名，并淡化其他所有内容；可过滤到单个数据库；可拖动节点、缩放、重新布局；键盘用户可以 Tab 到节点并按 Enter。

从 dashboard 的 **Schema Graph** 标签页打开它——只有点击时页面才会加载进 frame，因此其他人无需为它付出代价——或者直接从 bundle 文件夹打开该文件。为什么它是单独的文件、两个页面如何共享主题，以及凭据脱敏覆盖哪些内容，见 [Dashboard → Schema graph](#schema-graph-页面)。