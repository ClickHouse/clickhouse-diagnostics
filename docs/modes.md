# 模式：onprem、cloud 与 gov

*已从主 [README](../README.md) 移出，README 中保留每一节的简短版本。*

## 什么会离开你的机器

在把归档发给任何人之前：

- 工具**从不查询客户的行** — 每条查询都读取 `system` 表。自由文本列仍可能带有客户值（查询文本、异常消息、日志行）；当这一点很重要时，请参见 [Gov 模式与名称哈希](#gov-模式与名称哈希)了解哪些内容会被扣留或哈希。
- `configuration/` 已**剥离凭据**（密码、密钥、token、PEM 块），但保留主机名、IP、集群拓扑与表名 — 分享前请审阅。见[配置采集](bundle.md#配置采集)。
- `-dry-run` 会打印工具将运行的每个 SELECT，并附 `EXPLAIN ESTIMATE`，且不采集任何内容 — 先用它做安全审阅。见 [Dry-run 模式](running.md#dry-run试运行模式)。
- `gov` 模式用你的私有 salt 对数据库/表/用户/主机名做哈希，并扣留 dashboard、查询文本、配置、host facts（主机信息）与日志。**salt 和本地的 `*_gov_name_mapping.csv` 永远不会离开你的机器。**
- `alerts_summary.json`（当写出时）只包含规则名与计数，绝不包含匹配到的行。
- `keeper/` 保存 Keeper 自己的只读 four-letter command（四字命令）应答（`ruok`、`isro`、`lgif`、`srvr`、`mntr`、`conf`、`dirs`）：版本、Raft 日志位置、设置、延迟、未完成请求、节点数；在 gov 中 `conf` 只保留数字、布尔值与普通单词 — 路径、主机与客户自选名称一律显示为 REMOVED。列出客户端地址的命令（`stat`、`cons`）永远不会被发送；gov 会对成员的 host 做哈希。见 [Keeper 事实](bundle.md#keeper每个-keeper-成员对自身的描述)。

---

*以下各节是完整参考。*

## 模式与查询布局

**SharedMergeTree 自动切换。** 对 `cloud_mode = 1` 的服务器（自托管 SharedMergeTree 集群）执行 `-mode onprem` 运行时，会自动切换到 `queries.cloud/` 集合 — 每个按 replica 的 system 表（`query_log`、`part_log`、`errors`、`metric_log`、`text_log` …）在 `default` 集群上扇出，而共享表（`parts`、`tables`、`columns`、`databases`、`replicas`、`replication_queue`、`mutations`、`detached_parts`）像在任何 cloud 采集中一样从单个 replica 读取 — 同时仍从运行工具的机器采集 host facts、配置与日志文件（若你就在该机器上运行，则为本节点的；`warnings:` 头行会说明 `-host` 指向别处的情况），这一点仅 cloud 模式从不做。运行会说明这一点，`execution_log.txt` 记录 `collection: onprem → cloud：检测到 SharedMergeTree（cloud_mode = 1）`。这需要 `REMOTE` 与 `CREATE TEMPORARY TABLE ON *.*`；缺少它们时运行会停留在单节点并记录原因。`-single-node` 可禁用切换。头部还记录 `node:`（`hostName()`、`FQDN()`）与 `uptime:`，当重复探测返回不同主机名（负载均衡器）或当 `-host` 指向远程而本地文件正在被采集时，工具会发出警告 — 这两者都是 bundle 最终描述了错误机器的方式。

`-mode` 选择工具读取哪个顶层查询目录：

| 模式 | 查询目录 | 说明 |
|---|---|---|
| `cloud` | `queries.cloud/` | 使用 `clusterAllReplicas(...)` 在各 replica 间扇出 |
| `onprem` | `queries.onprem/` | 单节点的 `system.*` 引用 |
| `gov` | `queries.gov/` | on-prem 形态，PII 列做哈希；省略 `crash_log`/`stack_trace`（可能含敏感符号） |

> ⚠️ **运行二进制时，`queries.<mode>/` 目录必须存在于当前工作目录。** 工具把查询文件夹解析为*相对于你的 CWD* 的路径（例如 `./queries.onprem`），而不是相对于二进制自身。因此若你把二进制部署到另一台主机，必须把匹配的 `queries.<mode>/` 文件夹一并部署，并在运行前 `cd` 到包含两者的目录。
>
> 推荐的打包方式：
> ```bash
> # 本地构建 + 打包
> make release
> tar czf clickhouse-diagnostic.tgz \
>   bin/clickhouse-diagnostic-linux-amd64 \
>   queries.cloud queries.onprem queries.gov
>
> # 在目标主机上
> scp clickhouse-diagnostic.tgz host:/tmp/
> ssh host
> mkdir -p /opt/ch-diag && cd /opt/ch-diag && tar xzf /tmp/clickhouse-diagnostic.tgz
> ./bin/clickhouse-diagnostic-linux-amd64 -mode onprem ...   # 此时 CWD 中有 queries.* 文件夹
> ```
>
> 从 `/`、`~` 或任何没有 `queries.<mode>/` 作为同级目录的目录运行，会失败并报 `错误：queries 目录 './queries.<mode>' 不存在`。

告警查询使用相同的模式 — 见[告警](alerts.md#告警)。

> **自托管 SharedMergeTree 集群**（`system.settings` 中 `cloud_mode = 1`，表引擎为 `Shared*MergeTree`，磁盘为 `s3_with_keeper`）：每个 replica 保留自己的 `system.*` 表，因此 `-mode onprem` 描述的是 **N 个节点中的一个** — 只有它的 parts、错误、part_log、query_log 与 text_log。工具检测到 `cloud_mode = 1` 时会打印警告。要获得集群范围视图请运行 `-mode cloud`（用 `clusterAllReplicas` 在 `default` 集群上扇出；需要 `GRANT REMOTE` 与 `CREATE TEMPORARY TABLE`），并保留一次从单节点执行的 `onprem` 运行以获取 host facts、配置与日志文件。

### Gov 模式与名称哈希

在 `gov` 模式下，写入支持用输出的每个数据库名与表名都会被替换为 `hex(SHA256(name + salt))`。salt 由**你**在运行时提供（通过 `-salt` 或交互式提示）— 工具**不**附带默认值。这正是哈希有意义的原因：没有按客户区分的 salt，任何拿到源码的人都能为 `users`、`events` 或 `orders` 这类常见名称预先计算哈希，从而反推混淆。

**settings 类 collector 不做任何哈希** — 既不哈希设置名也不哈希值。设置值是服务器调优参数而非客户标识符，且下面的映射 CSV 是从 `system.tables` 构建的，因此哈希过的设置值只会是不可逆的噪声而不是保护。所以 `system.settings` 按服务器报告的原样输出每个值。在 `system.server_settings` 中，少数命名客户基础设施的值 — 任何形如路径或 URL 的值，加上 `default_database`、`default_replica_name`、`interserver_http_host`、`default_profile`、`merge_workload` 与 `mutation_workload` — 会被**移除**而非加盐，替换为 `REMOVED`（与配置 sanitiser（脱敏器）使用的同一哨兵值）。设置名、其 ClickHouse 默认值与 `changed` 都会保留，因此归档仍能显示*某个*路径被自定义过，而不显示它是什么。在 26.7 服务器上，即 439 行中移除 21 行、418 行可读。

**Salt 要求：**

- 8–64 个 ASCII 字母数字字符（`A–Z`、`a–z`、`0–9`）
- 无空格、无标点、无引号 — 以保证该值在 SQL 字符串字面量中安全
- 选择**无法从公开信息猜出**的内容（避免公司名、部署 ID，或支持工单里的任何内容）
- 若希望哈希在时间上可比较，**在各次运行中复用同一 salt**

**gov 模式下工具产出什么：**

```
clickhouse_results/
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS/                   # → 进入归档
│   └── *.jsonl                                              # 其中名称已哈希
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS.tar.gz            # → 归档本体，发送这个
└── clickhouse_diagnostic_YYYYMMDD_HHMMSS_gov_name_mapping.csv   # → 留在本地
```

映射 CSV（real_name → hashed_name）写在带时间戳的备份文件夹**之外**，因此构建归档时**不会**被 `tar` 收集。把它留在你的机器上用于自己的关联工作 — 并在发送归档前确认 salt 与 CSV 没有意外落入其中。

**与支持团队分享什么：**

| 文件 | 与支持团队分享？ |
|---|---|
| `clickhouse_diagnostic_*.tar.gz` | 是 |
| `clickhouse_diagnostic_*_gov_name_mapping.csv` | **否 — 留在本地** |
| salt 值本身 | **否 — 留在本地** |

若丢失 salt，过往归档中的哈希便不再可逆（你仍可用新 salt 运行新的诊断 — 但新旧哈希无法比较）。

### gov 模式不产出什么

若干输出在 gov 模式下被有意扣留，因为它们每个都属于支持用归档，且都无法有意义地哈希：

| 输出 | 原因 |
|---|---|
| `dashboard.html` | 其面板在 Go 中构建并选出原始标识符 — 最多 2000 张表的数据库/表名、磁盘路径、用户 — 以及服务器生成的文本（`last_exception`、`last_error_message`）。对每个面板做哈希是日后恢复它的后续工作。 |
| 查询分析（`--query-id` / `--normalized-query-hash`） | 该 bundle 内嵌原始查询文本、异常消息与完整 DDL。自由形式的 SQL 文本无法在不破坏诊断价值的前提下哈希，因此这些 flag 被直接拒绝。 |
| **`system.query_log_details_7_days` 中的查询文本** | onprem/cloud collector 会为每个 `normalized_query_hash` 归档一条采样查询的前 500 个字符，因此一旦服务器不在了，该文件中的哈希仍可映射回 SQL。查询文本内嵌客户字面量，所以 gov 变体完全省略该列 — 这是唯一无法做该映射的模式。 |
| **配置文件**（`configuration/`） | XML 内嵌原始主机名 — `<macros>` 的 shard/replica 名、`<remote_servers>`、`<zookeeper>` 主机。Sanitisation 剥离的是凭据而非标识符，且 XML 树在被哈希后无法保持可合并。 |
| **Host facts**（`host_info.json`） | 主机名、挂载路径与进程命令行正是 gov 哈希所保护的标识符 — 而命令行在被哈希后无法保持有用。 |
| **服务器日志文件**（`logs/`） | 日志行以自由文本携带原始查询、表名与路径。对日志做哈希会毁掉采集它的理由。 |
| **`--collect-text-log`** | 与日志文件同理：`system.text_log` 消息内嵌原始 SQL 与标识符。该 flag 在 gov 模式下被拒绝。 |

由于 dashboard 是告警结果的唯一其他消费者，gov 运行改为把 **[`alerts_summary.json`](alerts.md#alerts_summaryjson)** 写入归档 — 因此你仍能得到*“`too_many_parts` fired, 12 instances”*，而不含表名。

其余一切 — 按模式生成的结果文件 — 都按上述方式哈希。