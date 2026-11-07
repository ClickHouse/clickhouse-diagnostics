# 查询分析

*从主 [README](../README.md) 移出，该文件保留每节的简短版本。*

## 查询分析模式

当你已经知道**哪条**查询是问题时——来自客户工单的某个 `query_id`，或来自慢查询汇总的某个 `normalized_query_hash`——工具可以采集 `query_log`、`text_log` 和 `processors_profile_log` 的一个聚焦切片，让你理解它*为什么*慢，而无需取回整个系统。该分析**附加于**常规的按模式采集之上；它不会取代后者。

> **gov 模式下不可用。** 查询分析的输出会嵌入原始查询文本、异常消息、标识符和完整 DDL——正是 gov 模式哈希旨在保护的那些值，而它们无法在自由文本中有意义地哈希。因此在设置 `-mode gov` 时，`--query-id` / `--normalized-query-hash` 会被拒绝。

### 调用方式

| 你拥有的 | 传入 | 得到什么 |
|---|---|---|
| 一个具体的 `query_id`（来自工单） | `--query-id <uuid>` | 工具从 `system.query_log` 自动推导 `normalized_query_hash`，将时间窗口以该查询的 `event_time` 为中心，并同时运行单 id 查询和分组查询 |
| 一个 `normalized_query_hash`（来自 dashboard） | `--normalized-query-hash <uint64>` | 工具在窗口内为该 hash 自动推导**最慢**的 `query_id`（这样单 id 文件——ProfileEvents、text_log、被引用的表——才有内容），并运行完整 bundle |
| 两者都有 | 两个 flag 都传 | 跳过两项预检推导；除此之外与只传其一相同 |
| 一个具体的时间窗口 | `--from <RFC3339>` / `--to <RFC3339>` | 覆盖自动推导的窗口（接受 RFC3339 或 `YYYY-MM-DD`） |

在三种模式下都可用（`cloud`、`onprem`、`gov`）——表引用的适配方式与告警求值器相同（cloud 下为 `clusterAllReplicas(...)`，其他地方为普通 `system.*`）。

**Gov 模式注意事项**：`system.query_log` 中的 `query` 和 `tables` 列包含原始、*未哈希*的 SQL 和表名。标准的 gov 模式采集本就暴露了这一点；查询分析不会改变该暴露面。如果你的环境不能把原始 SQL 传出，请在 gov 模式下跳过分析相关的 flag。

### 采集内容

`queries.query_analysis/` 下有十二个 `.sql` 文件，每个写入 `<backup>/query_analysis/<name>_<ts>.<ext>`：

**单 query-id（需要 `--query-id`，或由 `--normalized-query-hash` 自动推导出的一个）**

| 文件 | 回答什么 |
|---|---|
| `query_details.sql` | 完整的 `query_log` 行——耗时、内存、读取行数、查询文本、异常、profile events |
| `profile_events.sql` | 本次执行的所有 `ProfileEvents`，按值降序排列。*最有用的单项产物。* |
| `text_log_parts.sql` | 仅包含 “Selected X/Y parts by partition key, Z marks…” 和 “Reading approx. N rows with M streams” 日志行——回答“我们是否做了全表扫描？” |
| `text_log_full.sql` | 该查询的每一行 `text_log`（最多 5000 行）——当定向切片未能显示问题时使用 |
| `tables_for_query.sql` | 该查询涉及的表的当前 DDL + 大小（从 `query_log.tables` 连接得到） |

**Hash 分组（需要 `--normalized-query-hash`，可由 `--query-id` 自动推导）**

| 文件 | 回答什么 |
|---|---|
| `fast_slow_query_ids.sql` | 窗口内该 hash 最慢和最快的 `query_id`——定义比较对 |
| `profile_events_compare.sql` | 慢执行与快执行的并排 `ProfileEvents`，含 `delta` 和 `percentage_diff` 列。*bundle 中最具诊断价值的查询。* |
| `hash_by_host.sql` | 按主机名的执行次数、avg / p95 / max 耗时、内存、错误——揭示“某个节点很慢”的模式 |
| `hash_summary.sql` | 每分钟执行次数（executions / succeeded / failed）。驱动 “Executions per minute” 堆叠条形图。 |
| `failed_over_time.sql` | 每分钟失败执行数，按异常码拆分——为 “Failed queries per minute” 堆叠条形图提供数据 |
| `failed_queries.sql` | 失败按表 × 错误拆分（涉及的 table、错误类型、user、count、first/last seen、sample exception） |
| `executions_timeline.sql` | 该 hash 每次单独执行一行（`LIMIT 10000`，最近的在前），包括 `ProfileEvents['UserTimeMicroseconds']`。驱动全部五个按执行的散点图（duration / memory / CPU / read rows / read bytes）。 |

### dashboard 集成

当设置了 `--query-id` 或 `--normalized-query-hash` 且未设置 `--skip-dashboard` 时，生成的 `dashboard.html` 会在导航顶部附近包含一个新的 **🔍 Query Analysis** 区块。

**聚焦头部**：哪个 `query_id`（用户提供或自动推导出的最慢者）、哪个 hash、时间窗口，加上聚焦执行的 duration / memory / read-rows，以及一行慢与快 `query_id` 耗时的对比（例如 “slowest 2400 ms · fastest 50 ms → 48× slower”）。

**查询文本卡片**（仅 cloud / onprem）：聚焦执行的确切 SQL，等宽字体、可滚动。在 `gov` 模式下隐藏——并从嵌入的 JSON 中剥离——因为查询文本包含 gov 模式本要哈希的表名。

**按执行的散点图**——五个图，每个单独查询一个点（最多 10 000 行，来自 `executions_timeline.sql`）。绿点 = 成功，红叉 = 失败。悬停显示 `query_id`、hostname、异常码和人类可读单位的指标值：

| 图 | Y 轴 | 说明 |
|---|---|---|
| 按执行耗时 | 秒或分（200 s 处自适应） | “为什么这一次执行慢”的视图 |
| 按执行内存使用 | MiB / GiB / TiB（自适应） | |
| 按执行用户 CPU | 秒 | 来自 `ProfileEvents['UserTimeMicroseconds']` |
| 按执行读取行数 | 行 | |
| 按执行读取字节 | MiB / GiB / TiB（自适应） | |

**计数条形图**——按分钟分桶（这是唯一不适用按执行的地方，因为指标*就是* “1”）：

| 图 | X | Y |
|---|---|---|
| Executions per minute | event minute | 计数，按 succeeded / failed 堆叠 |
| Failed queries per minute | event minute | 计数，按异常码堆叠（`MEMORY_LIMIT_EXCEEDED (241)`、`TIMEOUT_EXCEEDED (159)` 等） |

**单次执行行**：

- 聚焦执行的前 30 个 `ProfileEvents`
- 快与慢的 `ProfileEvents`（按 `|delta|` 取前 30）

**明细表**：

- 按主机分布（每个 hostname 的 executions / durations / memory / errors）
- 失败查询拆分（按 table × 错误类型 × user）
- 聚焦查询引用的表（当前 DDL + 大小）
- 最慢执行的 “Selected X parts, Y marks” 日志行
- 最慢执行的完整 `text_log`（可滚动）

当两个分析 flag 都未设置时，该区块隐藏。

### 示例

```bash
# 客户工单中有一个超时的 query_id。查看它，以及它与同一语句形态的
# 其他近期运行相比如何。
./clickhouse-diagnostic --mode cloud \
  --host my-cluster.clickhouse.cloud --port 8443 --protocol https --user default \
  --query-id 1bc3abaf-968f-4d4f-be3d-f77251b1ff0b \
  --skip-config -skip-archive
# → pre-flight：query_id ... → normalized_query_hash 7769688026807387533（event_time ...）
# → 查询分析：运行 12 个文件（窗口 <以 event_time 为中心的 48h>）
# → 12 个已写入，0 个跳过
# → dashboard.html 现在有 “Query Analysis” 区段
```

```bash
# dashboard 显示某个 hash 本周出现退化。仅在过去 7 天内运行
# 分组比较；不指定单个 query_id。
./clickhouse-diagnostic --mode onprem --host prod-ch-01 \
  --normalized-query-hash 7769688026807387533 \
  --from 2026-05-23 --to 2026-05-30
# → 12 个已写入，0 个跳过——工具会自动推导一个代表性的
#   query_id（窗口内最慢的一次执行），因此单 query_id 文件也会运行。
#   如果窗口内没有任何执行，则改为跳过 5 个单 query_id 文件
#   （7 个已写入，5 个跳过）。
```