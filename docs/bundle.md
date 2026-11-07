# bundle（诊断包）：输出格式、布局、host facts 与配置

*已从主 [README](../README.md) 迁移而来，该文件保留每一节的简短版本。*

## 输出格式

查询结果按每个查询一个文件写入，格式由 `-output-format` 选择。默认是 **`jsonl`**。

| 值 | ClickHouse 格式 | 扩展名 | |
|---|---|---|---|
| `jsonl` *(default)* | `JSONEachRow` | `.jsonl` | 每行一个自描述的 JSON 对象。可直接用 `grep` 和 `jq` 处理，且每一行都带有列名——这很重要，因为 `system.query_log` 约有 60 列，而按位置排列的格式会让你一路数着字段去对表头。 |
| `native` | `Native` | `.native` | v0.3.0 之前使用的 ClickHouse 二进制格式。面向列、类型精确、无需 schema 即可重新载入——但没有 ClickHouse 可供载入时无法读取。 |
| `tsv` | `TSVWithNamesAndTypes` | `.tsv` | 第 1 行是名称，**第 2 行是类型**。重新载入时最忠实的文本格式，但按位置排列，因此对宽表来说可读性很差。 |

### 查询在服务端受限

过去唯一的限制在客户端：5 分钟 HTTP 超时。这个超时与服务端毫无关系——一旦触发，Go 关闭连接，工具继续往下走，而服务端仍在执行。在 self-managed 上，`cancel_http_readonly_queries_on_client_close` 默认为 `0`，因此在慢集群上会不断累积被放弃的查询，每一个仍在消耗 CPU 和内存，而 collector（采集器）还在往上叠加更多。Cloud 将其设为 `1`，这就是为什么在 Cloud 测试中看不到这个问题，它只影响了该工具主要面向的 self-managed 用户。

现在每个请求都携带 `max_execution_time`（默认 240s，由 `-query-timeout` 设置）。服务端会强制执行它，因此慢查询会以一个干净的 `Code: 159` 失败，该错误会落到客户的 `query_log` 中并可被诊断，而不是消失在无声的断连里。HTTP 客户端等待的时间比 `-query-timeout` 指定的值多 60s，因此服务端总会在竞争中胜出。`-query-timeout=0` 会同时禁用两者。

还有两个设置一并生效。`cancel_http_readonly_queries_on_client_close=1` 告诉服务端在我们断开时丢弃查询——它只在 `readonly>0` 时生效，并且请注意它目前**不会**在 HTTPS 上触发：在优雅关闭时，服务端的存活探测会看到 TLS 的 `close_notify` 字节，并断定对端仍然存在（[ClickHouse#96737](https://github.com/ClickHouse/ClickHouse/issues/96737)）。它在普通 `http` 上有效，因此 `max_execution_time` 才是应当依赖的限制。`enable_http_compression=1` 允许服务端对响应做 gzip 压缩，这在链路较慢的堡垒机上很重要；Go 的 transport 会透明地请求并解压，如果任何人手动设置了 `Accept-Encoding`，它就会停止这样做。

### 大整数绝不被舍入

在大多数解析器中，JSON 数字是 IEEE-754 double，因此超过 2^53 的未加引号 `UInt64` 在读取时会被悄悄破坏——JavaScript 会把 `18446744073709551615` 变成 `18446744073709552000`，任何对它做算术的 `jq` 表达式也是如此。这不是假设：`normalized_query_hash` 以及 `system.query_log` 中每个 `cityHash64` 值都是全宽 `UInt64`。

因此工具在每个请求中固定 `output_format_json_quote_64bit_integers=1`，让 64 位整数以**带引号的字符串**形式输出，从而在任何解析器中都精确保留下来。该设置即使在 `readonly=1` 下也可写，所以服务端 profile 原本可能把它关掉——固定它才能让这一保证成立，而不是依赖服务端的配置。

`native` 和 `tsv` 在构造上就是精确的。重新载入时在读取阶段做类型转换：

```bash
clickhouse-local -q "SELECT toUInt64(normalized_query_hash) FROM file('x.jsonl','JSONEachRow')"
```

### 大小

交付产物是一个 `tar.gz`，JSON 中重复的键几乎可以被完全压缩掉。在一个真实 bundle（诊断包）上测得（21 个查询，ClickHouse 24.8）：

| 格式 | 未压缩 | **归档（`tar.gz`）** |
|---|---|---|
| `jsonl` | 2.20 MB | **225 KB** |
| `native` | 1.83 MB | 240 KB |
| `tsv` | 2.12 MB | 272 KB |

`jsonl` 在磁盘上大 20%，但生成的**归档最小**，因此在真正发送的内容上，这个可读的默认选项没有额外代价。

## host facts（主机信息）与服务端日志

除了 ClickHouse 对自身的报告之外，工具还收集通常能解释这些情况的周边上下文：它运行所在的主机，以及该主机磁盘上的日志文件。

### 何时运行

这两个 collector（采集器）读取的都是**执行工具的机器**，因此 `-host-info` 和 `-logs` 接受 `auto`（默认）、`on` 或 `off`，而不是一个简单的跳过 flag：

| 模式 | `auto` 解析为 | 原因 |
|---|---|---|
| `onprem` | **on** | 工具运行在 ClickHouse 服务端上，因此 `/proc` 和 `/var/log/clickhouse-server` 描述的就是该服务端。 |
| `cloud` | **off** | ClickHouse Cloud 通过网络访问。本地 host facts（主机信息）和日志文件描述的会是你的笔记本或堡垒机——支持 bundle（诊断包）中自信给出的错误数据比没有数据更糟。如果你把 cloud 模式指向一个 self-managed 节点，请传入 `-host-info=on` / `-logs=on`。 |
| `gov` | **off**，无条件 | 主机名、挂载路径、进程命令行和日志正文正是 gov 哈希所保护的内容，而它们都无法在保持有用性的同时被哈希。`-host-info=on` 会被**拒绝**，而不是被忽略。 |

两者在 `--dry-run`（试运行）下也会被跳过，因为该模式承诺不写入任何内容。模式矩阵由 `cmd/local_collector_test.go` 中的测试固定。

### `keeper/`——每个 Keeper 成员对自身的描述

`-keeper-mntr auto|on|off`（默认 `auto`）向 Keeper 成员发送七个只读 four-letter command（四字命令），并为每个成员写一个 `keeper/<host>_<port>.txt`。成员列表是两个来源的并集：`system.zookeeper_connection`，它给出每个已配置连接**当前所在**的成员（每个连接一行——默认的 `<zookeeper>` 块和每个辅助块——而不是每个 ensemble 成员一行）；以及工具在服务端运行时服务器配置（`-config-dir` 和相邻的 `config.xml`）中的每个 `<zookeeper><node>`。第二个来源才能触达此服务器*未*连接的 follower——Keeper 事故通常涉及的正是那个成员。无法访问配置的运行（远程 `-host`、cloud、没有 `-config-dir` 的 gov）只探测已连接的成员，并在执行日志中说明这一点。

| 命令 | 它回答什么 |
|---|---|
| `ruok` | 存活状态（`imok`） |
| `srvr` | Keeper 版本、延迟 min/avg/max、接收/发送、连接数、**未完成请求**、zxid、**模式**（leader / follower / standalone）、节点数 |
| `mntr` | 与 `zk_*` 计数器相同的内容，外加 follower 和已同步 follower（在 leader 上）、znode / watch / ephemeral 计数、内存、最新快照大小 |
| `isro` | 只读成员或 observer 返回 `ro`，否则返回 `rw` |
| `lgif` | 该成员的 Raft 日志：first / last index、committed 与 leader-committed、最后快照索引（23.x 之前不存在） |
| `conf` | 当前生效的协调设置——`snapshot_distance`、`reserved_log_items`、`snapshot_transfer_chunk_size`、`write_snapshot_version`……（在 gov 下只有数字、布尔值和普通单词会保留） |
| `dirs` | 快照和日志目录在磁盘上的字节数 |

先发送 `ruok`、`isro` 和 `lgif`，因为即使成员**未在服务**（没有 quorum、仍在加载快照）它们也能应答；其余命令随后会回复 `This instance is not currently serving requests`，这会被记录为该成员的状态。每个命令都是只读的，完全不涉及 Raft，也不随数据量增长：在健康成员上每个约 0.3 ms。成员是**并行**询问的，某个成员在第一次网络失败后就不会再收到任何请求，因此一个或几个挂起的成员总共只消耗一次读取超时（10 s），而不是每个命令一次。有副作用的命令（`csnp`、`rqld`、`ydld`、`rcvr`、`srst`、`crst`、`rclc`、`clrs`）绝不会被发送。每个文件都会标明其 ensemble（`# ensemble: default`，或辅助 ZooKeeper 的名称——在 gov 下会被哈希），因此不同 ensemble 的成员绝不会被比较；辅助成员的文件名为 `<ensemble>__<host>_<port>.txt`，因此指向同一 host:port 的默认连接和辅助连接绝不会互相覆盖。

这些就是用来定论某个 Keeper finding（发现）的服务端事实：`metric_log` 说明*此服务器*等待 Keeper 了多久，`zookeeper_connection` 说明它在与哪个成员通信，而只有 Keeper 自己才能说明该成员是否为 leader、队列中有多少请求以及它运行着什么。每个命令都会打开自己的 TCP 连接（3 s 连接，10 s 读取），因为 Keeper 在一次应答后就会关闭连接；一个宕机或拒绝四字命令的成员会在其文件和 `execution_log.txt` 中如实记录，绝不会导致整次运行失败。`stat` 和 `cons` **绝不会被发送**：它们会列出客户端地址。

| 模式 | `auto` 解析为 | 原因 |
|---|---|---|
| `onprem` | **on** | 工具运行在集群旁边，因此 Keeper 端口可达。 |
| `gov` | **on**，主机名哈希 | 计数器都是数字；唯一的标识符是成员的主机名，它会在文件名和文件头中写作 `hex(SHA256(host ‖ salt))`——与 gov bundle（诊断包）中 `system.zookeeper_connection` 所携带的形式相同，因此两者可以关联。任何携带 `ip:port` 的回复行都会被丢弃，作为保护措施。 |
| `cloud` | **off** | 托管服务的 Keeper 无法从外部访问。对于在 cloud 模式下采集的 self-managed 集群，`-keeper-mntr=on` 会被执行，但会给出警告。 |

在 `--dry-run` 下只列出成员，不联系任何成员；用于构建列表的 `system.zookeeper_connection` 上每连接一行的 `SELECT` 是一次元数据预检读取，会真实执行并像版本探测一样打印出来。`execution_log.txt` 中的每成员状态：`ok`（全部三个命令都有应答）、`partial`（部分应答——通常是因为 `four_letter_word_white_list` 中缺少某个词）、`refused`、`timeout`（包括被 10 s 截止时间截断的回复，会保留在文件中并标记为截断）、`failed`（所有命令都因其他原因失败，或文件无法写入）。23.8 之前的服务器没有 `system.zookeeper_connection`，因此该步骤会被跳过并说明原因。Keeper 必须在 `four_letter_word_white_list` 中允许这些词（默认的 `*` 可以）；空回复会被相应地标注。

### `host_info.json`——操作系统、内核与硬件

从 `/proc`、`/sys` 和 `/etc` 读取，不依赖外部组件，因此只有当工具**在 ClickHouse 主机上**运行时才会产生数据——这就是 `auto` 将其限制在 onprem 的原因。在其他位置强制启用时，每一节都会标记为不可用并附说明，而不是悄悄留空。

| 节 | 内容 |
|---|---|
| `os` | 主机名、**发行版 + 版本**（`/etc/os-release`）、内核版本和完整 banner、架构、运行时间 |
| `cpu` | 逻辑 CPU 数、型号、厂商、负载均值，以及 ClickHouse 进行分派所依据的向量 flag（`avx2`、`avx512f`……/ ARM 上的 `asimd`、`sve`） |
| `memory` | 总量 / 空闲 / 可用 / buffers / cached / swap，单位为字节 |
| `disks` | 每个真实挂载点：设备、挂载点、文件系统类型、总量、空闲、已用百分比（伪挂载和文件绑定挂载已过滤） |
| `top_processes_by_rss` | 按 RSS 排名前 25——pid、ppid、state、threads、RSS、command |
| `clickhouse_relevant_tunables` | **透明大页** + defrag、`vm.swappiness`、`vm.overcommit_memory`、`vm.max_map_count`、`fs.nr_open`、`fs.file-max`、**cgroup 内存/CPU 限制**，以及正在运行的服务端自身的 `LimitNOFILE` |

最后一行正是这个 collector（采集器）的意义所在：THP 被设为 `always`、cgroup 内存上限远低于 `MemTotal`，或 `LimitNOFILE` 过低，能解释相当大一部分 ClickHouse 事故，而这些问题从数据库内部是看不见的。

### `logs/`——服务端日志文件

```bash
./clickhouse-diagnostic -mode onprem -host ch-01           # 发现日志目录
./clickhouse-diagnostic -mode onprem -logs-dir /data/ch/logs
```

目录按以下顺序解析：

1. `-logs-dir`，原样使用（错误路径会被报告，绝不会悄悄替换）
2. **从服务器配置中读取的 `<log>` / `<errorlog>` 路径**——运维人员迁移日志目录的频率高于改动其他任何东西，这正是避免 bundle（诊断包）悄悄不含任何日志的原因
3. `/var/log/clickhouse-server`

当第 2 项和第 3 项不同时，两者都会被采集。默认复制 `*.log`；`-logs-include-archives` 会增加轮转后的 `*.gz`/`*.zst`。超过 `-logs-max-mb`（默认 50）的文件会被**从尾部**截断——保留最近的一端，并在文件头记录丢弃了什么，以免第一行保留下来的内容被误认为日志开头。

> 在 `<level>debug</level>` 下，繁忙的服务器**几秒**就能写入 50 MB，因此 `clickhouse-server.log` 的尾部很少能回溯到数小时前已结束的事故（`.err.log` 的尾部通常覆盖几个小时）。要还原事故时间线，请依赖 `system.text_log_histogram_1_day` 和按小时的聚合，只有需要某段特定日志时才提高 `-logs-max-mb`（或用 `-logs-include-archives` 一并带上轮转文件）。

### `--collect-text-log`——`system.text_log` 的有界切片

**默认关闭且要求显式窗口**，因为 `text_log` 是 ClickHouse 写入量最大的表：无界转储会非常庞大，并且本身就会给服务器带来负载。

```bash
./clickhouse-diagnostic -mode onprem -host ch-01 \
  -collect-text-log --from 2026-08-20T14:00:00Z --to 2026-08-20T15:00:00Z \
  -text-log-level Warning
```

| Flag | 含义 |
|---|---|
| `-collect-text-log` | 启用它。**需要 `--from` 和 `--to`**——否则运行会快速失败。 |
| `-text-log-level` | 最低严重级别（`Fatal`……`Trace`）。省略则包含所有级别。 |
| `-text-log-limit` | 行数上限（默认 500 000）。 |

写为 `text_log_<timestamp>.<ext>`，采用[配置的输出格式](#输出格式)。在 cloud 模式下会分发到各 replica（副本）；`message_format_string` 仅在 23.1+ 上被选取。如果 `system.text_log` 被禁用（ClickHouse 的默认行为），工具会说明这一点并解释如何启用，而不是只报一个裸错误。

## 配置采集

未设置 `-skip-config` 时，工具从 `-config-dir` 读取文件（默认 `/etc/clickhouse-server/config.d/`；在 `cloud` 模式下除非显式给出 `-config-dir`，否则跳过采集，而 `gov` 模式从不采集），并将 sanitisation（脱敏）后的副本写入本次运行的 `configuration/` 目录（位于 `clickhouse_diagnostic_<timestamp>/` 内），镜像源目录树——`config.d/storage.xml` 和 `users.d/storage.xml` 保持区分，文件来源目录（决定 ClickHouse 的合并顺序）也得以保留。

sanitisation（脱敏）分两层进行——先做正规的 XML / YAML 解析，再对结果做一次启发式字节模式扫描。如果文件无法解析，工具会**失败关闭（fail closed）**：记录一条警告并跳过该文件，而不是把未脱敏的文件发出去。

### 哪些内容会被脱敏

**结构化**（由标签 / 属性 / YAML 键名驱动，不区分大小写）：

- 任何与以下精确列表匹配的名称：`client_id`、`tenant_id`、`account_name`、`account_key`、`storage_account_url`、`role_arn`、`role_session_name`、`session_token`、`connection_string`
- 任何**包含**以下之一的名称：`password`、`passwd`、`secret`、`credential`、`token`、`private_key`、`api_key`、`access_key`、`service_account`——因此像 `<my_db_password>`、`<gcp_service_account_credentials>`、`<legacy_api_token>` 这样的未来或自定义标签无需改代码就会被捕获
- 遵循相同命名规则的 XML 属性值：`password="..."`、`aws_access_key_id="..."` 等
- 上述标签内的多行值（此前仅用正则的实现会漏掉这些）

**启发式**（字节形态特征，应用于整个文件，包括注释和自由文本）：

- PEM 封装的私钥（`-----BEGIN ... PRIVATE KEY-----` 块）
- AWS 访问密钥 ID（`AKIA…`、`ASIA…`）
- JWT token（`eyJ…` 三段式形态）
- Bcrypt 密码哈希（`$2[abxy]$…`）
- 长十六进制字符串（40+ 字符——SHA-1、SHA-256 及更长）
- 长 base64 数据块（128+ 字符——GCP 服务账号载荷等）
- 嵌入 URL 的凭据（`proto://user:pw@host`——只脱敏密码，主机名保留）
- 正文中的 `keyword: value` / `keyword = value` / `keyword "value"` 泄露，其中 keyword 包含 `password`、`secret`、`token`、`api_key`、`access_key`、`private_key` 或 `credential`——可捕获粘贴到 XML 注释或 YAML `# notes` 中的值

### 哪些内容**不会**被脱敏

- 主机名、IP、集群拓扑
- 数据库名和表名
- 性能 / 资源设置
- 非安全相关配置
- 提到凭据关键词但没有实际值的正文（例如 `password requirements are documented elsewhere`）

该启发式有意偏向过度脱敏而非脱敏不足——输出中多标记一个哈希无伤大雅，泄露凭据则不是。分享归档前，请始终检查本次运行 `configuration/` 目录的内容。

## 输出布局

一次运行会生成：

```
clickhouse_results/
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS/                   # → 归档
│   ├── system.parts_YYYYMMDD_HHMMSS.jsonl                   #   每个查询一个文件
│   ├── system.mutations_YYYYMMDD_HHMMSS.jsonl
│   ├── ...
│   ├── configuration/                                       #   脱敏后的配置，目录树保留（除非 -skip-config；gov 下从不包含）
│   │   ├── config.xml
│   │   ├── config.d/…
│   │   └── users.d/…
│   ├── query_analysis/                                      #   仅在使用 --query-id / --hash 时
│   ├── keeper/<host>_<port>.txt                             #   每个 Keeper 成员的 four-letter command（四字命令）回复（-keeper-mntr；gov 下主机名会哈希；cloud 默认不包含）
│   ├── dashboard.html                                       #   除非 -skip-dashboard 或 gov
│   ├── execution_log.txt                                    #   每个 collector（采集器）：结果、耗时、大小；alert rule（告警规则）；阶段
│   └── alerts_summary.json                                  #   当 alert rule（告警规则）运行了但没有 dashboard.html 时
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS.tar.gz             # → 归档文件，除非 -skip-archive
└── clickhouse_diagnostic_YYYYMMDD_HHMMSS_gov_name_mapping.csv   # → 仅本地（gov 模式）
```

- **查询结果**：每个查询一个文件，格式由 [`-output-format`](#输出格式) 选择（默认 `jsonl`）
- **Dashboard（仪表盘）**：独立的 `dashboard.html`，从 CDN 加载 Chart.js
- **执行日志**：`execution_log.txt`——每个 collector 查询一行，记录其版本目录、结果（`ok` / `failed` / `empty`）、耗时、结果字节数和行数，外加每条 alert rule（告警规则）的结果和耗时、每个被探测 Keeper 成员的一条 `keeper` 记录（`ok` / `partial` / `refused` / `timeout`），以及每个阶段的耗时（collector、keeper facts、host facts、logs、config、alerts、dashboard）。*最耗时的 collector* 列表是在调整 `queries.<mode>/` 中窗口之前应当查看的内容；*失败的 collector* 列表则用于区分“表是空的”和“查询根本没运行”。它只包含文件名、计时和 ClickHouse 错误文本——不含结果数据。
- **归档**：包含每次运行结果目录的 `tar.gz`——`configuration/` 现在位于其*内部*，目录树完整，因此一个 bundle（诊断包）永远只可能包含本次运行的配置。（在 v0.3.0 之前，它是运行目录旁边一个扁平的、进程级的 `./configuration`；任何按该路径解析 bundle 的逻辑都需要更新。）
- **Gov 模式映射 CSV**（仅 gov 模式）：位于备份文件夹旁边，**不在其内部**——绝不会进入归档。参见 [Gov 模式与哈希名称](modes.md#gov-模式与名称哈希)。