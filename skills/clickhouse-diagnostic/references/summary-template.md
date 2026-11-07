# 摘要模板——分析输出的样子

除非用户要求生成文件，否则直接在对话中输出摘要（markdown）。保持易于扫读：读者应在10秒内得到结论、2分钟内看到证据。每条finding都以`file → column → value`引用其证据。写测量结果，而不是归因（见 §Rules，即下方"文本规则"）。

```markdown
# ClickHouse 诊断摘要 — <bundle 文件夹名>

**覆盖范围。** ClickHouse <version> · mode <cloud|onprem|gov> · node <header 中的 hostName() | unknown> 采集时已运行 <uptime> <若采集前 24 小时内重启过，写明> · <collection: onprem → cloud (SharedMergeTree) | one node of N | 省略> · 采集于 <运行时间戳, tz> · query_log 窗口 <min> → <max>（<N> 个小时桶）· text_log <N> 行，覆盖 <first>–<last> · 缺失/空文件：<列表或 none> · host facts：<描述该服务器 | 未采集 | 描述的是另一台机器（已忽略）> · logs：<文件，是否截断？>。
<如果用户的事件不在窗口内，在这里说明，并给出重新采集命令。>

## 概要
- <一行：最重要的 finding，带数字>
- <一行>
- <一行——或“没有 critical 或 warning 级别的 finding；详情如下。”>

## 健康 scorecard（评分卡）
| 区域 | 状态 | 证据 |
|---|---|---|
| 可用性与崩溃 | ok / warning / critical / n/a | `system.crash_log`：0 行 |
| Parts 与 merges | … | `system.parts`：<db>.<table> 分区 <id> 有 812 个 active parts（告警阈值 300） |
| 复制与 Keeper | … | `system.replicas`：最大 absolute_delay 4 秒；Keeper 健康：每小时硬件异常均为 0，事务数 ≈ 中位数（或：13:00–17:00 不可用——每小时 2500 万次异常，流量为中位数的 9%） |
| 磁盘与存储 | … | `system.disks`：default 剩余 71%；`system` 库 38 GiB（第 3 大） |
| 内存与 CPU | … | `metric_log.avg_memory_tracking_bytes` 峰值 11.2 GiB / 16 GiB RAM；load 0.3 / 8 CPU |
| 查询负载 | … | 241 = 查询总数的 3.1%（1 240/40 000），一个 hash 占 92% |
| 插入 | … | `part_log` NewPart 3 900/小时，平均 240 行/part |
| Mutations 与 TTL | … | 2 个 mutation，最老 6 小时，parts_to_do 3 |
| 字典 | … | 4 LOADED，0 FAILED |
| 配置与主机可调参数 | … | THP = always（warning）；overcommit 0；nofile 500000 |
| 日志 | … | err.log：最高错误码 241（312 次），999（0 次）；0 次 Fatal |

## Finding（最严重优先）
### F1 — <标题，先写症状>  ·  <critical|warning|info>  ·  HC-2.1, P-01
**是什么：** <用平实语言写一两句话>
**证据：**
- `system.parts_2*.jsonl` → <db>.<table>/<partition_id> 中的 active parts：812（level 0：540）
- `system.part_log_3_days_*.jsonl` → 2026-08-25 10:00–14:00 期间 NewPart 3 900/小时，MergeParts 41/小时
- `system.metric_log_7_days_*.jsonl` → max_merge_pool_tasks = 16（池大小）持续 6 小时
**为何重要：** <后果，以及下一个会触发的阈值：“插入从 1000 个 part 起变慢，达到 3000 个时被拒绝（`parts_to_throw_insert`）”>
**建议操作：** <有序、具体，带设置与默认值；区分立即缓解与长期修复>
**置信度：** high | medium | low — <原因；什么能确认它>

### F2 — …

## 建议（按影响/成本排序）
1. <操作> — 解决 F1、F3 — 成本低
2. …

## 建议的后续提示词
- “分析 normalized_query_hash 为 <hash> 的查询形态——用 `--normalized-query-hash <hash> -from … -to …` 重新运行工具，识别形态（MergeTree 读取、表函数、分布式语句的 worker 子查询），从 ProfileEvents 构建时间预算，然后才比较慢速与快速运行。”
- “对照本 bundle 中前 5 个查询形态，审查 <db>.<table> 的 ORDER BY 与分区键，并提出 schema。”
- “按类别（query / total / user / merge）解释 MEMORY_LIMIT_EXCEEDED 消息，并把每一类映射到一个设置。”
- “按表比较插入速率（NewPart/h）与 merge 吞吐，并提出批大小。”
- “为 part 最多的表起草 TTL 与 merge 设置变更，并附回滚说明。”
- “哪些系统日志表没有 TTL，它们占用了多少磁盘；写出配置变更。”
- “给定版本 <v>，列出 <v> 与该 minor 最新补丁 / 最新 LTS 之间的不兼容变更和相关 bug 修复。”（需要网络）
- gov：“把这些哈希后的表名与我的本地映射 CSV 关联，并重新标注 finding”（在本地运行）。

## 需要重新采集的内容（仅在需要时）
- <来自 running-the-tool.md §8 的精确命令，以及原因>

## 注意事项
- <覆盖范围限制、哈希名称、估算值、任何推断而非实测的内容>
```

## 文本规则

- **测量vs归因。** 每个因果句都必须有已声明置信度的测量事实作为支撑。"Memory peaked at 11.2 GiB while 2 merges of `<table>` ran (peak_memory_usage 4.8 GiB each)" 是测量；"the merges caused the OOM" 是归因——在第二个来源确认之前，用 "consistent with" 来表述。
- **数字优先于形容词。** 写 "812 parts (threshold 300)"，而不是 "many parts"。始终给出对比所用的阈值及其来源（alert rule、ClickHouse默认值、指导值）。
- **一段 = 一行。** 不要在段落或列表项内手动折行；由渲染器负责折行。段落要短（≤ 3句）。
- **引用文件，不引用感觉。** 每条证据要点都以bundle文件（glob形式即可）和列名开头。
- **版本感知。** 引用设置时给出*该*版本的默认值；不确定时说 "verify in `Settings.cpp` at tag <v>"，而不是猜测。
- **描述bug/issue的措辞：** "resembles"、"consistent with"、"a fix was merged in PR #N (first in vX.Y) — worth confirming"；绝不用 "known issue"（见`clickhouse-source.md` §4）。
- **除非被要求，否则不写面向客户的话术。** 如果被要求，添加`**Suggested reply:**`标题，后接一个包含回复正文的围栏代码块（便于干净粘贴），保持事实性，并先执行隐私检查清单（`privacy.md`）。
- **链接**只用`[title](url)`指向公开文档/GitHub；若没有直接相关的内容，省略该节。
- **简洁。** headline不超过3条；scorecard每个区域一行；findings不超过7条（其余归入 "Also observed" 列表）；建议不超过7条。

## 可选：HTML伴生页

如果用户希望在`dashboard.html`旁再有一个可分享的页面，就把同一份markdown渲染为独立的`summary.html`（不依赖外部资源），放在bundle的*父*目录，而不是bundle文件夹内——归档应保持工具产出的原样。渲染前按`privacy.md`脱敏。
