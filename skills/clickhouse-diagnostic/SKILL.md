---
name: clickhouse-diagnostic
description: >-
  分析ClickHouse support-diagnostic bundle（clickhouse_diagnostic_*.tar.gz，或旧版本的
  clickhouse_backup_*.tar.gz，或其解压目录：system.* JSONL转储、dashboard.html、
  host_info.json、logs/、configuration/），产出有证据支撑的健康摘要——parts
  与merges、复制与Keeper、磁盘、内存/CPU、查询、插入、
  mutations/TTL、主机可调参数——并给出建议操作与后续提示。
  同时说明如何运行clickhouse-diagnostic（grants、模式、窗口、
  查询分析、gov mode）以及需要重新采集哪些内容。当用户提到
  diagnostic bundle、support bundle、clickhouse_diagnostic、clickhouse_backup、dashboard.html、
  "run the diagnostic"、"what does this file in the bundle mean"，或询问为什么
  其ClickHouse服务器变慢、OOM、parts过多、处于只读状态或
  正在丢失插入，并且已有（或可以采集）bundle时使用。可离线工作；仅在做可选的
  版本研究时访问GitHub。
user-invocable: true
---

# clickhouse-diagnostic — 读取bundle，解释服务器

bundle（诊断包）由`clickhouse-diagnostic`（本仓库）生成。它是**一台ClickHouse服务器或服务的只读快照**：约20张`system.*`表（JSONL格式）、alert结果、可选的host facts、配置和服务器日志。你的任务是把这份快照变成一份简短、有证据支撑、所有者可据此行动的摘要，并明确说明bundle无法展示什么。

## 唯一准则：证据优先，bundle原封不动

每一条finding（发现）都要引用`file → column → value`。绝不修改bundle。绝不把bundle内容发送给任何服务，也绝不把标识符粘贴到任何离开本机的内容中——除非先通过隐私闸门（第7步）。永远不要把任何问题称为 "known issue"——应说 "resembles" / "consistent with"，由所有者或ClickHouse确认。如果数据未覆盖该问题，就直说，并给出精确的重新采集命令，而不是猜测。

## 何时使用 / 何时不适用

- **使用**场景：`clickhouse_diagnostic_*.tar.gz`（旧版本为`clickhouse_backup_*.tar.gz`）或其解压目录；关于bundle中某个文件/列含义的问题；"如何运行该工具 / 需要哪些grants / 用哪个模式"；"针对这个问题应该采集什么"。
- **不适用**场景：对正在运行的服务器做实时排障（没有bundle）——可以提议说明如何采集一份；ClickHouse之外的问题（应用代码、Kafka broker、云IAM）超出bundle能展示的范围。

## 动手前先读表（惰性加载——只读当前步骤需要的内容）

| 何时 | 阅读 |
|---|---|
| 打开bundle中任何文件之前 | `references/bundle-layout.md`（归档里有什么、列、编码） |
| 决定从某个文件里算什么，或它"健康"时是什么样 | `references/file-guide.md` — 每个文件一个条目：为什么运行它、它回答什么问题、先读清单、健康基线、危险信号、陷阱；另有场景走查（高负载下的复制集群、TOO_MANY_PARTS、241、磁盘、升级回归、崩溃） |
| 对JSONL编写任何query/`jq`/Python | `references/reading-recipes.md` |
| 第2步（健康检查） | `references/health-checks.md` |
| 出现错误码（`system.errors`、`exception_code`、`Code: NNN`） | `references/error-codes.md` |
| 某条finding与某个症状吻合（parts、内存、Keeper、mutations……） | `references/known-patterns.md` — 扫描其速查表，只打开匹配的条目 |
| 用户询问其版本、某个设置的默认值、某个bug或升级（需要网络） | `references/clickhouse-source.md` |
| 用户没有bundle、时间窗口不对或数据缺失 | `references/running-the-tool.md` |
| 撰写摘要 | `references/summary-template.md` |
| 任何文本或文件离开本机之前 | `references/privacy.md` |

## 工作流程

### 0 — 接收与覆盖范围（下任何结论前必做）
1. 定位输入：一个`.tar.gz`，或一个包含`system.version_*.jsonl`的目录，或一个父目录`clickhouse_results/`（其中有一个`clickhouse_diagnostic_*`，旧版本为`clickhouse_backup_*`）。如果有多个，先询问。
2. 运行确定性的预扫描——它会安全解压（到临时目录）、清点文件，并计算不需要SQL的检查项：
   ```bash
   python3 <skill-dir>/scripts/inspect_bundle.py <bundle.tar.gz|dir>          # markdown 输出
   python3 <skill-dir>/scripts/inspect_bundle.py <bundle> --json > /tmp/inspect.json
   ```
   `<skill-dir>`是存放本SKILL.md的目录。后续所有recipe都复用打印出的解压目录（`B=…`）。

   如果它以退出码2退出并输出`bundle was collected with -output-format native|tsv`，**立即停止**：整个skill只读取`.jsonl`，该bundle后续的任何结论都不可信。告知该bundle不可读，并要求用默认的`-output-format jsonl`重新采集（bundle-layout §1）。绝不要自行解读`.native`/`.tsv`文件来替代，也绝不要从这些文件报出零值。
3. 用不超过6行说明**覆盖范围**：ClickHouse版本；模式（cloud/onprem/gov——判定规则见bundle-layout §3）；system表描述的是**哪个节点**、该节点已运行多久（`execution_log.txt`头部的`node:` / `uptime:`——采集前24小时内重启过的节点属于重启后证据；`warnings:`行表示collector看到了负载均衡器或混主机运行；`collection:`行表示onprem运行在SharedMergeTree集群上切换成了cloud采集，或停留在单节点及其原因）；采集时间戳；query_log实际覆盖的时间窗口；text_log的行数与时间跨度；为空/缺失的文件及原因（健康为空vs未采集）；`TRUNCATED`日志头部；`host_info.json`描述的是本服务器还是另一台机器；collector缺少grant的迹象（只能看到`system`表、`system.errors`中出现497）；以及当服务器有materialized view时，是否采集了`query_views_log`——存在MV但没有该文件时，预扫描会给出HC-0警告，因为此时"没有MV finding"意味着没有遥测，而不是管道健康。
4. 如果用户的故障不在覆盖窗口内，现在就说明，并提议用`-from/-to`重新运行（running-the-tool §8）。然后基于bundle实际覆盖的内容继续。

### 1 — 对headline做分诊
从预扫描结果和`dashboard.html`的`DATA.alerts` / `alerts_summary.json`中提取：已触发的alert（绝不要把"无法运行"计为finding）、`crash_log`行数、只读replica、可用空间 < 15% 的磁盘、按运行时长（`asynchronous_metrics.Uptime`）衡量的top `system.errors`错误码、`replication_queue.last_exception`、**事故时段**——dashboard的Keeper Health判定表（与HC-3.8同一规则）、`metric_log_coordination`出现Keeper硬件异常的时段、`part_log`只有插入没有merge的时段，或`query_log`异常激增的时段（预扫描会打印按小时的时间线）。写出1–3条headline要点；其余都是细节。

### 2 — 分区域健康检查
按顺序走`health-checks.md` HC-1 → HC-11，并在出现任何999/319/571、只读replica或merge停滞的时段时运行 **Keeper健康测试**（HC-3.8：硬件异常*和*每小时事务数对比7天中位数）；对接触到的每个文件打开`file-guide.md`中的对应条目，并使用`reading-recipes.md`中的recipe（有`clickhouse local`就用它，否则用Python；`jq`只用于非数值过滤）。每个区域记录一个状态，并附上一条最具体的证据行。注意两个数据陷阱：`query_log_details`的行会按表重复（`LEFT ARRAY JOIN tables`）——求和前先固定一个`tables`值；`system.errors.value`是自重启以来的累计值——要与uptime对比，绝不能当作速率。读取`system.parts`时只用`active = 1`。

### 3 — 查询分析（仅当存在`query_analysis/`时）
先读`file-guide.md` → *query_analysis/*。顺序很重要：
1. **识别查询形态**（依据`query_details`）：`tables`（一张MergeTree表，或`_table_function.s3Cluster`/`s3`/`remote`之类的表函数）、`is_initial_query`、`query_kind`、`user`/`initial_user`。如果`is_initial_query = 0`，该hash属于某个分布式语句的**worker子查询**——所谓"执行"是一次运行的各个分片，慢与快的差异衡量的是数据倾斜，而不是回归。要说明这一点，并按replica份额分析。
2. **时间预算**：从重点执行的`profile_events`中取`query_duration_ms`作为墙钟时间（不要用`RealTimeMicroseconds`，那是按线程求和的），对比`OSCPUVirtualTimeMicroseconds`（消耗的CPU）、`OSCPUWaitMicroseconds`（被限流的CPU）、`NetworkSendElapsedMicroseconds`（传输行数）、`ParquetFetchWaitTimeMicroseconds`/`ReadBufferFromS3Microseconds`/`S3ReadMicroseconds`（对象存储）、`DiskReadElapsedMicroseconds`、`MemoryTracker*`。先说出占主导的组件，再谈其他。
3. **多次运行vs分片**：`executions_timeline` + `hash_by_host`——不同主机上在相同`ts`聚成一簇、行数与时长成比例 = 分片；在任何主机上分散于不同时间 = 多次运行。
4. 只有当第3步判定为"多次运行"时才看`profile_events_compare`（最大的`|delta|`）；只有当`tables`是MergeTree表时才看`text_log_parts`（"Selected N/N parts" = 未做裁剪）和`tables_for_query`；用`text_log_full`定位起始点（`Reading object…`、`Selected…`、行间最长间隔）；用`failed_*`确定错误开始的时间。
5. 对不适用的查询形态，空文件是正常的（表函数 → `text_log_parts`、`tables_for_query`；无失败 → `failed_*`）；应说"不适用"，而不是"未采集"。
把结论对应到P-50…P-56。

### 4 — 模式匹配
对每条warning/critical级别的finding，扫描`known-patterns.md`的速查表并打开匹配条目。在把匹配结果升级为建议之前，先用bundle中第二个独立来源执行其 *Verify* 步骤。措辞规则见`clickhouse-source.md` §4。

### 5 — 版本研究（可选，需要网络，先征得同意）
仅在用户需要、或某条finding看起来与版本相关（P-30、P-31、P-54）时才做：把`version`解析为tag，检查客户补丁版本与该minor最新补丁之间的release notes，在对应tag的源码中核实设置，用精确的错误文本搜索issue。遵循四步引用协议；绝不要编造issue编号。

### 6 — 撰写摘要
使用`summary-template.md`：覆盖范围 → headline → scorecard → findings（按严重程度排序，每条包含证据、阈值、操作、置信度）→ 建议 → **建议的后续提示** → 需要重新采集的内容 → 注意事项。写测量结果，不写归因。数字要带阈值。findings不超过7条。

### 7 — 隐私闸门
在任何输出被粘贴进工单、分享，或写入scratch目录之外的文件之前：执行`privacy.md`中的检查清单（主机名、IP、库/表/列/用户名、查询字面量、路径、配置片段）。绝不要索取gov salt；映射CSV只在本地使用。不要写入bundle目录。

## 运行工具（当用户没有bundle时）

让他们阅读`references/running-the-tool.md`：grants（`SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.*` + `SELECT ON system.*`；cloud模式还需`REMOTE` + `CREATE TEMPORARY TABLE`）、`-mode onprem|cloud|gov`、在包含`queries.<mode>/`的目录中运行、用`-from/-to`指定事故窗口、用`--normalized-query-hash`/`--query-id`针对单个查询、用`-dry-run`做安全审查。在他们把任何内容发给第三方之前，说明这次运行会采集什么、不会采集什么。**密码：** CLI依次取`-password`、环境变量`CLICKHOUSE_PASSWORD`、TTY提示——在agent shell中提示读取为空，运行会以401 / code 194失败。优先使用`export CLICKHOUSE_PASSWORD=…`且不传`-password`（这样它不会出现在`ps`中）；绝不要在同一行写`VAR=x cmd -password "$VAR"`，绝不要把密码写进仓库，并提醒用户事后轮换密码（running-the-tool.md §2a）。

## 硬性禁止

- **不要**在未固定`tables`值的情况下对`query_log_details`跨行求和（会重复计数）。
- **不要**把`system.errors.value`当作速率；要把它与uptime或另一份bundle配对使用。
- **不要**把带引号的64位整数解析为JavaScript/`jq`数字。
- **不要**根据`system.text_log`得出"没有错误"的结论（24小时、2000行上限）——要检查它实际的时间跨度。
- 当工具运行在与服务器不同的机器上时，**不要**信任`host_info.json`。
- **不要**把来自SharedMergeTree / `cloud_mode = 1`集群的`onprem` bundle当作整个集群来读，除非其`execution_log.txt`头部写着`collection: onprem → cloud`（v0.7+ 在grants允许时会自行切换）——即便有这一行，也只有按replica采集的表（query_log、part_log、errors、metric_log、text_log……）覆盖每个replica；`parts`、`tables`、`replicas`、`replication_queue`、`mutations`、`detached_parts`只是某一个replica的副本。没有这一行，它就是N个replica中的一个（parts、errors、part_log、query_log、text_log都是按replica的）：要说明是哪个节点（`node:`头部），并提议授予`REMOTE` + `CREATE TEMPORARY TABLE`后重跑，或使用`-mode cloud`。
- 头部带负载均衡器 **`Warning:`** 的bundle，**不要**当作任何单一节点的全貌：每张system表都来自当时应答该查询的那个replica。前缀很关键——在cloud模式下同一行是 **`Note:`**，因为Cloud端点本来就按设计在服务的各replica之间做负载均衡，没有可优先选择的按replica端点，采集也是为此设计的：按replica的表会扇出，共享表只读一次。读这条note是为了知道*哪个* replica提供了共享表，而不是作为不信任该bundle的理由。
- **不要**仅凭`query_log`得出"Keeper没问题"的结论：后台merges、fetches和part提交从不出现在那里。要先读`metric_log_coordination_3_days`（每小时的`ZooKeeperHardwareExceptions`）和`part_log`的错误。
- **不要**把 **essential** bundle（`execution_log.txt`的`collect: essential …`、dashboard横幅、预扫描的第一条finding）当作完整bundle来读：它的窗口只有1天，且按表采集的collector（parts、tables、columns、detached parts、clusters、stack_trace……）是故意跳过的——文件缺失说明不了健康状况。之所以退化为essential（负载、内存、并发、只读replica、无Keeper会话），本身就是一条finding；等节点恢复健康后要求用`-collect full`重新采集。
- **不要**把缺失的`zookeeper_log_errors_1_day` / `blob_storage_log_7_days`当作任何证据——这两张表只在配置后才存在。
- **不要**把缺失的`aggregated_zookeeper_log_*`文件当作证据——它们只从 ≥ 26.3的服务器采集。如果存在且怀疑涉及Keeper，要在向客户索取`zookeeper_log` **之前**先读它们（谁 / 何时 / 何地——HC-3.21–3.23、P-62–P-68）；对于早于其窗口的事故，把`reading-recipes.md` §3b中的实时查询交给客户。
- **不要**把任何问题称为已知问题、说出你未核实过的修复版本，或引用你未打开过的issue。
- **不要**在面向客户的文本中把测量结果升级为归因。
- **不要**修改、重新打包或上传bundle；不要把`configuration/`、`logs/`、`host_info.json`或DDL原文粘贴到本机之外。
- **不要**依赖alert的*名称*来判断错误码——要核对错误码（2026年9月之前的工具版本带有一条`too_many_simultaneous_queries`规则，实际过滤的是252 = TOO_MANY_PARTS；还有一条`too_many_parts`消息写成 "code 497"，而497是ACCESS_DENIED；这些版本产出的bundle会带有标注错误的alert文本）。

## 输出风格

简洁。短段落，一行一个观点，不手动折行。scorecard和证据用表格；推理用散文。用glob引用bundle文件（`system.parts_*.jsonl`）。以后续提示结尾，让用户能用一条消息再深入一层。