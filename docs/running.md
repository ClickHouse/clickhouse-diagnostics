# 运行工具

*已从主 [README](../README.md) 移出，README 中保留每一节的简短版本。*

## 用法

> 这些示例使用 `./clickhouse-diagnostic`。若你通过 `make` 构建，二进制位于 `./bin/clickhouse-diagnostic` — 请调用该路径或为它建立符号链接。若还没有二进制，见[安装](building.md#安装)。

### 必需的 grant（授权）

工具是只读的，从不读取客户表 — 每条查询都读取 `system` 表。这只约束了它*查询*什么，而非什么会进入 bundle（诊断包）：部分 `system` 表会以自由文本描述客户数据（查询文本、异常消息、日志行），因此当这一点很重要时，请参见 [Gov 模式与名称哈希](modes.md#gov-模式与名称哈希)了解哪些内容会被扣留或哈希。`onprem` 与 `gov` 模式只需两个 grant：

```sql
CREATE USER sys_read_only IDENTIFIED WITH sha256_password BY '<password>';

GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO sys_read_only;
GRANT SELECT ON system.*                               TO sys_read_only;
```

```
┌─GRANTS FOR sys_read_only────────────────────────────────────────────────┐
│ GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO sys_read_only │
│ GRANT SELECT ON system.* TO sys_read_only                               │
└─────────────────────────────────────────────────────────────────────────┘
```

| Grant | 为什么需要 |
|---|---|
| `SELECT ON system.*` | 每条诊断查询、alert rule（告警规则）与 dashboard（仪表盘）面板都读取 `system` 表。`system` 之外的对象永远不会被选中。 |
| `SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.*` | ClickHouse 会把对象列举类的 `system` 表过滤成用户持有*某种*权限的部分。缺少 `SHOW DATABASES`/`SHOW TABLES` 时，工具只能看到集群的一部分；缺少 `SHOW COLUMNS` 时，`system.columns` 会返回**你的数据库没有任何行**，而其他每个文件看起来都正常。 |

> **不要用数据数据库上的 `SELECT` 来替代。** `GRANT SELECT ON <db>.*` 同样能填充那些列举表，收集到的 bundle 完全一样 — 在 26.2.19.43 上测得，两种 grant 组合下 `system.tables` 为 2、`system.columns` 为 5、`system.databases` 为 2、`system.parts` 为 1 — 但它把客户数据的读权限交给了诊断用户，而 `SHOW` grant 不会（在 `SHOW` grant 下对用户表执行 `SELECT` 会被 497 拒绝）。

> **缺少 `SHOW` grant 不会报错 — 它会静默地截断结果。** 每条查询仍然报告成功；bundle 只是描述了服务器的一小部分。在只有一个用户数据库的测试实例上测得：
>
> | 表 | 有该 grant | 没有该 grant |
> |---|---|---|
> | `system.tables` | 141 | 112 |
> | `system.databases` | 5 | 1 |
> | `system.columns` | 3258 | 2520 |
> | `system.parts` | 102 | 101 |
>
> 这就是要警惕的失败模式：一个看起来完整、却缺少客户自有表的 bundle。若 `system.databases` 中只有 `system`，说明缺少 `SHOW` grant — 工具现在会在启动时发现 `system` 之外没有任何数据库时打印警告，但较旧的二进制不会。
>
> **两个** grant 都缺时，运行仍会完成：受保护的 system 表以 `Code: 497 … Not enough privileges` 失败（可在 `execution_log.txt` 中看到），而 `system.tables`、`system.columns` 和 `system.databases` 返回的是**空文件**而非错误，因此归档只有几百 KB，什么都描述不了。发送前请与其他节点的归档大小对比。

两个 grant 都不会暴露客户数据：`SHOW` 只揭示对象*名称*与元数据，而 `SELECT` 被限定在 `system` 上。

#### cloud 模式还需要两个

`cloud` 模式把查询包进 `clusterAllReplicas(...)`，使结果覆盖每个 replica（副本），而不只是你连接的那个节点。每个请求都以 `skip_unavailable_shards = 1` 运行：下线的 replica 会被略过，而不是让每个按 replica 的 collector（采集器）都失败，bundle 中的 `system.replicas_answered` 会说明哪些 replica 没有应答。分布式执行需要另外两个 grant：

```sql
GRANT REMOTE                ON *.* TO sys_read_only;
GRANT CREATE TEMPORARY TABLE ON *.* TO sys_read_only;
```

因此 cloud 的完整集合是：

```
┌─GRANTS FOR sys_read_only────────────────────────────────────────────────────────────────────────────────┐
│ GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS, CREATE TEMPORARY TABLE, REMOTE ON *.* TO sys_read_only │
│ GRANT SELECT ON system.* TO sys_read_only                                                               │
└─────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

与 `SHOW` grant 不同，这些缺失时会明确报错，而不是截断：

```
Code: 497. DB::Exception: Not enough privileges. To execute this query,
it's necessary to have the grant REMOTE ON *.*. (ACCESS_DENIED)

Code: 497. DB::Exception: Not enough privileges. To execute this query,
it's necessary to have the grant CREATE TEMPORARY TABLE ON *.*. (ACCESS_DENIED)
```

`onprem` 与 `gov` 都不需要 — 通过撤销两者并重新运行已验证（分别为 21/21 与 19/19 条查询，0 错误）。

#### grant *不*覆盖的内容

配置收集、`host_info.json` 和 `logs/` 读取的是**本地文件系统**，而非数据库，因此它们依赖的是操作系统权限：对 `/etc/clickhouse-server/`、`/var/log/clickhouse-server/` 和 `/proc` 的读权限。见[主机信息与服务器日志](bundle.md#host-facts主机信息与服务端日志)。

### 交互式运行

```bash
./clickhouse-diagnostic
```

命令行未提供的任何值都会被提示输入：

| 提示 | 默认值 |
|---|---|
| 协议（http/https） | `http` |
| ClickHouse 主机 | `localhost` |
| ClickHouse 端口 | `8123`（http）/ `8443`（https） |
| 用户名 | _空_ |
| 密码（隐藏输入） | _空_ |
| 模式（cloud/onprem/gov） | `onprem` — 仅当完全没有 `-mode` 时；见下文 |
| 配置目录 | `/etc/clickhouse-server/config.d/` — 仅 `onprem`；`cloud` 与 `gov` 不会询问（见下文） |
| Gov 模式 salt（隐藏输入，仅 `gov` 模式） | _空_ |

#### `-mode` 如何解析

`onprem` 是三种模式中保护性最弱的，所以工具绝不会*猜测*它。无法解析的 `-mode` 值是硬错误并以非零码退出 — 它不会被向下归为 `onprem`，否则拼错的 `-mode gov` 会在一个要求哈希的运行中收集未哈希的数据库名与表名，而在脚本化采集中没有人会读那条警告：

```
$ clickhouse-diagnostic -mode govv ...
模式 "govv" 无效——请选择 cloud, onprem, gov；你是指 "gov" 吗？
$ echo $?
1
```

`onprem` 默认值只在完全没有 `-mode` 时生效。在终端中你会被提示，按 Enter 即接受；当 stdin 关闭时，会使用默认值并说明：

```
No -mode given and stdin is not a terminal; using onprem.
```

值会被去除空白并转为小写，且接受以下拼写：

| 模式 | 另被接受 |
|---|---|
| `cloud` | `ch-cloud`、`clickhouse-cloud` |
| `onprem` | `on-prem`、`on_prem`、`on-premise(s)`、`onpremise(s)`、`self-hosted`、`selfhosted`、`self_hosted` |
| `gov` | *无* — 保护性模式不接受别名，因此近似拼写会被拒绝，而不是靠猜测解析 |

### 命令行 flag

运行 `./clickhouse-diagnostic -help` 查看完整列表。当前 flag：

```
-host string           ClickHouse 主机
-port string           ClickHouse 端口
-user string           用户名
-password string       密码（出于安全原因不推荐）
-protocol string       协议（http 或 https）
-mode string           查询模式（cloud、onprem、gov）（默认 "onprem"）
-output-dir string     结果输出目录（默认 "./clickhouse_results"）
-config-dir string     要采集的 ClickHouse 配置目录
                       （默认：/etc/clickhouse-server/config.d/）
-alerts-dir string     包含 alert YAML 规则文件的目录（默认 "./alerts"）
-salt string           gov 模式的哈希 salt（8–64 个字母数字字符；
                       为空时交互式提示；仅 gov 模式）
-query-id string       运行以该 query_id（UUID）为中心的查询分析。
                       见下文“查询分析模式”。
-normalized-query-hash string
                       运行以该 normalized_query_hash（UInt64）为中心的
                       查询分析。可与 --query-id 组合，也可单独使用。
-from string           查询分析的时间窗口起点（RFC3339 或 YYYY-MM-DD）。
                       设置 --query-id 时默认“最近 3 天”，仅设置
                       --normalized-query-hash 时默认“最近 7 天”。
-to string             查询分析的时间窗口终点（默认：当前时间）。
-analysis-dir string   包含查询分析 SQL 文件的目录
                       （默认 "./queries.query_analysis"）
-output-format string  查询结果的序列化格式：
                       jsonl|native|tsv（默认 "jsonl"）。
                       见下文“输出格式”。
-host-info string      采集主机 OS/CPU/内存/磁盘/进程信息：
                       auto|on|off（默认 "auto"——仅 onprem 为 on）。
                       见下文“host facts 与服务器日志”。
-logs string           从磁盘采集 ClickHouse 服务器日志文件：
                       auto|on|off（默认 "auto"——仅 onprem 为 on）。
-logs-dir string       存放服务器日志的目录（默认：从服务器配置中发现，
                       然后回退到 /var/log/clickhouse-server）
-logs-max-mb int       采集日志的单个文件上限，单位为 MiB（默认 50）。
                       更大的文件会被尾部截断。
-logs-include-archives 同时采集轮转日志（*.gz、*.zst）。默认关闭。
-keeper-mntr string    向 system.zookeeper_connection 中的每个 Keeper 成员
                       请求其只读 four-letter 回复，并写入 keeper/<host>_<port>.txt：
                       auto|on|off（默认 "auto"——onprem 和 gov 为 on，
                       cloud 为 off）。见下文“Keeper 信息”。
-collect-text-log      采集 system.text_log 中限定时间的一片数据。
                       需要 --from 和 --to；gov 模式会被拒绝。
-text-log-level string --collect-text-log 的最低严重级别
                       （Fatal|Critical|Error|Warning|Notice|Information|
                       Debug|Trace）。默认：所有级别。
-text-log-limit int    --collect-text-log 的行数上限（默认 500000）
-dry-run               列出将执行的每个查询（包括各自触及的系统表
                       以及每个 SELECT 的只读 EXPLAIN ESTIMATE）后退出。
                       不写结果、不创建归档包。见下文“Dry-run 模式”。
-skip-config           跳过采集配置文件
-skip-alerts           跳过评估 alert rule
-single-node           在 SharedMergeTree 集群（cloud_mode = 1）上，让 -mode onprem
                       只采集本节点，而不是切换到通过
                       clusterAllReplicas(default, ...) 的 cloud 采集
-skip-dashboard        跳过生成 HTML dashboard
-skip-archive          跳过创建结果与配置的归档包
```

命令行中留空的任何 flag 都会被交互式提示输入（`-skip-*` 开关以及 `-output-dir` / `-alerts-dir` 除外，它们会静默使用默认值）。

### 示例

对 Cloud 集群运行（`cloud` 模式会自动跳过配置文件：服务器的配置目录不在运行工具的机器上；传入 `-config-dir` 可仍收集一个）：

```bash
./clickhouse-diagnostic -mode cloud -host my-service.us-east-1.aws.clickhouse.cloud \
  -port 8443 -protocol https -user default
```

对 on-prem 节点运行，把所有内容写入自定义目录：

```bash
./clickhouse-diagnostic -mode onprem -host prod-ch-01 \
  -output-dir ./diagnostics-2026-05-13
```

只跑查询的快速检查（无告警、无 dashboard、无归档）：

```bash
./clickhouse-diagnostic -mode onprem -host localhost \
  -skip-alerts -skip-dashboard -skip-archive
```

政府 / 哈希化 PII 模式，并使用非默认的 alerts 目录：

```bash
# Salt 会以隐藏输入交互式提示。若要显式传入：
./clickhouse-diagnostic -mode gov -host gov-ch-01 \
  -alerts-dir ./alerts.gov -salt myDeployment2026
```

> **Gov 模式需要 salt** — 见 [Gov 模式与名称哈希](modes.md#gov-模式与名称哈希)了解它的作用以及为什么需要它。

完全非交互（CI / 自动化）：

```bash
./clickhouse-diagnostic -mode cloud \
  -protocol https -host $CH_HOST -port 8443 \
  -user $CH_USER -password $CH_PASS \
  -skip-config
```

> **安全：** 优先使用交互式密码提示或环境变量，而不是 `-password` — flag 会出现在 `ps`/shell 历史中。

## Essential 或 full 采集（`-collect`）

服务器出问题时，恰恰是最需要诊断的时候，也恰恰是 full 采集代价最高的时候。采集之前，工具会运行一个短暂的负载探测（约一秒，仅内存表）并决定采集多少：

| `-collect` | 会发生什么 |
|---|---|
| `auto`（默认） | **Full** 采集，除非探测看到服务器在挣扎 — `SELECT 1` 慢于 1 s、`system.metrics` 慢于 2 s、运行中的查询达到 `max_concurrent_queries` 的 80 %、内存达到服务器上限的 90 %、负载平均值高于核心数的 2×，或存在只读的复制表。此时改为 **essential**。full 运行在**运行中途**若某条查询超时或连续两条耗时超过 30 s，也会切换为 essential。 |
| `essential` | 始终 essential。 |
| `full` | 始终 full — 无论探测结果如何都不切换。 |

**essential** 采集只运行标记为 `-- tier: essential` 的 collector（版本、指标、进程、错误、replica 与 replication queue（复制队列）、merge（合并）、mutation（变更）、Keeper 会话、磁盘，以及最近一天的 `query_log` / `part_log` / `metric_log` / `error_log` / `text_log`），并附带：

- 每个 3 天与 7 天窗口缩减为 **1 天**（显式的 `-from` / `-to` 仍然优先）；
- 每查询 **30 s** 超时（除非给出了 `-query-timeout`），以及整个运行 **3 min** 的预算（用 `-time-budget` 修改）— 到时仍未运行的会被跳过并列出；
- `max_threads = 2`、`max_memory_usage = 1 GiB` 与最低的 `priority`，每项都会先检查，若用户的 settings profile 拒绝则略去；
- 读取每张表的 dashboard 面板被略去；源文件未被采集的 alert rule 会被报告为*不适用*，并指明被跳过的 collector。

当探测在**存在只读复制表却没有任何 Keeper 会话**时，那些等待会话的查询（`system.zookeeper_connection`、`system.database_replicas`、`system.replicas_problem_tables`、dashboard 的 Keeper 面板 — 以及随之读取这些文件的告警）会被跳过，而不是等到超时，而 Keeper four-letter command（四字命令）会从服务器配置中取得其成员。

在**大型 SharedMergeTree** 服务器上（10 000+ 张表），`auto` 运行只会为非共享表以及 broken-part 日志行中提到的共享表列出 detached part（分离 part）；`-collect full` 会列出每张表。

该决定及其原因是 `execution_log.txt` 中的 `collect:` 行、`dashboard.html` 顶部的横幅，以及分析技能预检的第一个 finding（发现）。每个被跳过的 collector 都会出现在执行日志中，状态为 `skipped` 并附原因。

## 采集窗口

大多数采集查询会回顾一个固定时段。每条查询声明**自己的**默认值，因此某条扫描异常昂贵（或异常便宜）的查询可以单独说明，而不影响其余查询：

| 查询 | 默认回顾时长 |
|---|---|
| `system.query_log_details_7_days` | 7 天 |
| `system.query_log_exceptions_1_day` | 1 天 |
| `system.query_log_by_hour_7_days` | 7 天 |
| `system.query_log_by_pattern_7_days` | 7 天 |
| `system.part_log_3_days` | 3 天 |
| `system.query_views_log_3_days` | 3 天 |
| `system.metric_log_7_days` | 7 天 |
| `system.asynchronous_insert_log_7_days` | 7 天 |
| `system.metric_log_coordination_3_days` | 3 天 |
| `system.metric_log_by_host_3_days` | 3 天 |
| `system.blob_storage_log_7_days` | 7 天 |
| `system.error_log_7_days` | 7 天 |
| `system.distributed_ddl_queue` | 7 天 |
| `system.text_log` | 1 天 |
| `system.text_log_histogram_1_day` | 1 天 |
| `system.text_log_keeper_1_day` | 1 天 |
| `system.zookeeper_log_errors_1_day` | 1 天 |
| `system.aggregated_zookeeper_log_by_hour_2_days` | 2 天 |
| `system.aggregated_zookeeper_log_by_minute_1_day` | 1 天 |
| `system.aggregated_zookeeper_log_paths_1_day` | 1 天 |

> `system.text_log` 与 `system.zookeeper_log` 止于 1 天：它们是这里数据量最大的表（繁忙集群每小时写入数百万行 Keeper 日志），7 天的切片太大，放在支持 bundle 中没有用处。需要更多时请使用 `-from`。

`-from` 与 `-to` 会一次性覆盖**每一个**窗口：

```bash
# 特定事故时间窗内的所有内容
./clickhouse-diagnostic -mode onprem -host prod-ch-01 \
  -from 2026-08-14T09:00:00Z -to 2026-08-14T13:00:00Z

# 将回顾时长放宽到 30 天，而不是每条查询的默认值
./clickhouse-diagnostic -mode onprem -host prod-ch-01 -from 2026-07-21
```

任一端都可单独给出：只有 `-from` 而无 `-to` 时运行到当前时间，只有 `-to` 而无 `-from` 时保留每条查询自己的默认起点。解析后的窗口会在运行开始时打印。时间为 RFC3339 或 `YYYY-MM-DD`，按 UTC 解释。

当设置了 `-query-id` / `-normalized-query-hash` 时，同样的 flag 也驱动[查询分析](query-analysis.md#查询分析模式)。

[Alert rule](alerts.md#告警) 保留自己的回顾时长（例如*“最近一小时的异常尖峰”*）— 这是规则语义的一部分。它们读取已采集的文件，因此有了 `-to`，该回顾时长就在 `-to` 结束，也就是文件结束处；比某条规则的回顾时长更窄的 `-from` 只会给它已采集的那些小时。

> **在缓慢或繁重的采集上，第一个要尝试的就是缩窄窗口。** 默认值是为一般健康检查调优的；在繁忙集群上，用 `-from` 指定几小时会便宜得多。

### 给查询作者

窗口是模板占位符而非字面量，因此保持可覆盖：

```sql
WHERE event_time >  {from:7d}      -- 若给出 --from 则用之，否则 now() - INTERVAL 7 DAY
  AND event_time <= {to:now}       -- 若给出 --to 则用之，否则 now()
```

接受的默认值为 `now`，以及 `<N>d` / `<N>h` / `<N>m`。格式错误的 spec（`{from:7dd}`）**不会**被替换 — 查询会被拒绝，而不是静默地无界运行。有一个测试断言没有任何随附查询硬编码间隔，因此新增的硬编码查询会让 CI 失败，而不是悄悄忽略这些 flag。

## Dry-run（试运行）模式

注重安全的客户可以传入 `-dry-run`，查看工具将执行哪些查询、针对哪些表，**而不**进行任何实际数据采集：

```bash
./clickhouse-diagnostic -mode cloud -host my-service.region.aws.clickhouse.cloud \
  -port 8443 -protocol https -user default \
  -dry-run
```

`-dry-run` 会做什么：

- 列出会被发送的每个 SELECT（基于文件的、查询分析 bundle，以及任何没有 `check:` 的 alert rule；随附的 alert rule 与 dashboard 读取已采集的文件，不发送任何查询）
- 为每条查询标注它触及的 `system.*` 表
- 在每个 SELECT 下添加一个只读的 `EXPLAIN ESTIMATE` 块 — 报告该查询**将**扫描的 rows/marks/parts，但不读取任何 data part。见 [EXPLAIN ESTIMATE 文档](https://clickhouse.com/docs/sql-reference/statements/explain#explain-estimate)。
- 将空估算渲染为 `this table is empty`（规划器确认没有内容匹配谓词）
- 自动跳过 `-skip-config` 与 `-skip-archive`（不产生副作用文件）

dry-run 中仍会到达服务器的内容：

| 调用 | 原因 |
|---|---|
| `SELECT version()` | 为服务器版本选择正确的查询变体 |
| `--query-id` / `--normalized-query-hash` 的预检 | 推导哈希 + event_time（或最慢的 query_id），使打印出的分析 SQL 有真实值，而非未绑定的 `{query_id}` 标记 |
| `SELECT DISTINCT host, port FROM system.zookeeper_connection`（`-keeper-mntr`） | 列出 Keeper 成员，使 dry run 能说明它会联系哪些成员；运行时打印 |
| 每个 SELECT 的 `EXPLAIN ESTIMATE <query>` | 仅只读元数据 |

与查询分析 flag 组合，也可对聚焦 bundle 进行 dry-run：

```bash
./clickhouse-diagnostic -mode cloud -host ... \
  -dry-run \
  -normalized-query-hash 15477159632099527852
```

输出样例块：

```
[28]
    Tables: system.query_log
    SQL:
      SELECT ts, query_id, query_duration_ms, ...
      FROM system.query_log
      WHERE query_id = '30df7836-fe07-4f42-9d52-832a69abbb8b'
        AND event_time >= '2026-05-28 09:06:21'
        AND event_time <= '2026-06-04 09:06:21'
    EXPLAIN ESTIMATE:
      ┌─database─┬─table─────┬─parts─┬─rows─┬─marks─┐
      │ system   │ query_log │     2 │   14 │     2 │
      └──────────┴───────────┴───────┴──────┴───────┘
```