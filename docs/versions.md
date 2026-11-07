# 支持的版本与版本特定查询

*从主 [README](../README.md) 移出，该文件保留每节的简短版本。*

## 版本特定查询

在每个模式目录内，你可以通过将查询放入名为 `MAJOR.MINOR.PATCH.BUILD` 的子目录，为特定 ClickHouse 版本覆盖查询。工具会选择不高于所连接服务器的最高版本。

```
queries.onprem/
├── system.detached_parts.sql     # 默认（从 22.8 起可用）
├── 22.11.1.0/
│   └── system.detached_parts.sql # 增加 bytes_on_disk/path（服务器 ≥ 22.11）
└── 23.11.1.0/
    └── system.detached_parts.sql # 还增加 modification_time（服务器 ≥ 23.11）
```

无法解析为版本的目录会被跳过。

同样的约定适用于 `queries.query_analysis/`（`.sql` 文件）和 `alerts/`（`.yaml` 规则）：将同名文件放入 `MAJOR.MINOR.PATCH.BUILD` 子目录，即可为处于或高于该版本的服务器覆盖根版本。

```
alerts/
├── detached_parts_exist.yaml     # 默认规则（从 22.8 起可用）
└── 22.11.1.0/
    └── detached_parts_exist.yaml # 增加 bytes_on_disk（该列在 22.11 加入）
```

**仅**存在于版本子目录中的文件（没有根目录对应文件）在较旧服务器上会被直接跳过——用于查询尚不存在的系统表（例如 `system.asynchronous_insert_log`，在 22.10 加入）。

> **覆盖按基本文件名匹配，区分大小写。** 版本子目录中的文件只有在名称完全一致（包括大小写）时才覆盖根文件。如果你在版本目录中重命名了文件——或只是改变了大小写（`Foo.yaml` 与 `foo.yaml`）——它会被视为*独立的*查询/规则（两者都会运行），而不是覆盖。打算覆盖时，请保持文件名与根文件逐字节相同。

## 支持的 ClickHouse 版本

工具面向 on-prem 服务器的 **ClickHouse 22.8 及更新版本**。根级查询和告警文件只使用 22.8 可用的列和语法；更新的内容放在版本子目录之后。当前的版本门控（已对照 ClickHouse changelog 验证）：

| 特性 | 加入版本 | 门控位置 |
|---|---|---|
| 对 `Map`（`ProfileEvents`）执行 `ARRAY JOIN` | 22.8 之后 | 根文件使用 `mapKeys()`/`mapValues()`（所有版本） |
| `system.asynchronous_insert_log` 表 | 22.10 | `queries.{onprem,gov}/22.10.1.0/` |
| `system.disks.unreserved_space` | 22.10 | `queries.{onprem,gov}/22.10.1.0/` |
| `system.detached_parts.bytes_on_disk`、`path` | 22.11 | `queries.{onprem,gov}/22.11.1.0/`、`alerts/22.11.1.0/`（gov 仅门控 `bytes_on_disk`——它从不采集 `path`/`disk`） |
| `GROUP BY ALL` 语法 | 22.12 | 根文件使用显式键列表 |
| `dateDiff('millisecond', …)` 亚秒单位 | 22.12 之后 | 异步延迟使用 `*_microseconds` 的浮点减法 |
| `system.text_log.message_format_string` | 23.1 | `queries.query_analysis/23.1.1.0/` |
| `system.server_settings` 表 | 23.3 | `queries.{onprem,gov}/23.3.1.0/`（无根文件——低于 23.3 时跳过；cloud 在根级携带它） |
| `system.asynchronous_insert_log.rows` | 23.4 | `queries.{onprem,gov}/23.4.1.0/`（22.10–23.3 报告 `bytes`） |
| `system.settings.default` | 23.4 | `queries.{onprem,gov}/23.4.1.0/`（`default` 是它们根文件唯一省略的列；cloud 根文件有它） |
| `system.clusters` 的 replicated-db 列（`database_shard_name`、`database_replica_name`、`name`；有意不选择 `is_active`） | 23.5 | `queries.*/23.5.1.0/` |
| `system.query_log.query_cache_usage` | 23.8 | `queries.query_analysis/23.8.1.0/` |
| `system.zookeeper_connection` 表 | 23.8 | `queries.*/23.8.1.0/`（无根文件——所有模式下低于 23.8 时跳过） |
| `system.zookeeper_connection.xid`、`last_zxid_seen`、`availability_zone` | 24.3 | `queries.*/24.3.1.0/`；`alerts/24.3.1.0/keeper_xid_renewal_due.yaml`（无根规则——低于 24.3 时不适用） |
| `system.query_log.peak_threads_usage` | 23.9 | `queries.query_analysis/23.9.1.0/` |
| 系统日志表中的 `hostname` 列 | 23.11 | `queries.*/23.11.1.0/`（根文件使用 `hostName()`） |
| `system.blob_storage_log` 表（需要 `<blob_storage_log>` 配置） | 23.11 | `queries.*/23.11.1.0/`（无根文件——所有模式下低于 23.11 时跳过） |
| `system.tables.total_bytes_uncompressed` | 23.12 | `queries.query_analysis/23.12.1.0/` |
| `system.view_refreshes` 表 | 23.12 | `queries.*/23.12.1.0/`（无根文件——所有模式下低于 23.12 时跳过） |
| `system.mutations.is_killed` | 24.1 | `alerts/24.1.1.0/`（根文件省略该过滤） |
| `background_*_pool_size` 从 `system.settings` 移到 `system.server_settings` | 23.3 | `alerts/23.3.1.0/`（`fetch_pool_saturated`、`schedule_pool_saturated`；在较新服务器上 `system.settings` 仍以旧的会话默认值列出这些名称，因此根规则在那里会拿错误的 size 做比较） |
| `system.tables.metadata_version` | 24.2 | `queries.*/24.2.1.0/` |
| `system.error_log` 表 | 24.8 | `queries.*/24.8.1.0/`（无根文件——所有模式下低于 24.8 时跳过） |
| `system.database_replicas` 表 | 25.9 | `queries.*/25.9.1.0/`（无根文件——所有模式下低于 25.9 时跳过） |
| 带 `component`（26.2）和 `is_subrequest`（26.3）的 `system.aggregated_zookeeper_log` | 26.3 | `queries.*/26.3.1.0/`（无根文件——所有模式下低于 26.3 时跳过：该表从 25.9 起存在，但在 26.3 之前子请求与独立请求共享桶，并携带整个 Multi 的延迟） |
| `system.zookeeper_log.duration_microseconds`（取代 `duration_ms`） | 24.3 | `queries.*/24.3.1.0/`（根文件使用 `duration_ms`；输出在每一层级都保持 ms） |
| `system.tables.parameterized_view_parameters` | 25.4 | `queries.{onprem,cloud}/25.4.1.0/`（gov：不采集） |
| `system.tables.target_database`、`target_table`（MV 的写入目标，包括隐式 `.inner_id.*` 表） | 26.6 | `queries.*/26.6.1.0/`（gov 对两者都做哈希） |

dashboard（`internal/dashboard/generator.go`）动态构建其 SQL，因此它不使用版本目录，而是在运行时探测实时 schema（`hasColumn`/`hasTable`）并适配每个 panel——覆盖相同的列（`error_count`、`is_killed`、`bytes_on_disk`、异步表/`rows`、`crash_log`）以及可能被配置禁用的可选表。

`queries.cloud/` 面向托管的 ClickHouse Cloud，它始终运行较新版本——因此携带较新的层级（`23.5.1.0/`、`23.11.1.0/`、`24.2.1.0/`、`25.4.1.0/`），但不包含任何低于 23.5 的门控，因为 Cloud 从不运行那么旧的版本。**22.8 下限适用于 `queries.onprem/` 和 `queries.gov/`**（两者都是自托管——gov 是带哈希 PII 的 on-prem，且都不使用 `clusterAllReplicas`），也适用于共享的 `queries.query_analysis/` + `alerts/` 目录。注意 gov 的 `system.tables` 层级刻意止于 `24.2.1.0/`——它不采集 `parameterized_view_parameters`（否则 gov 将不得不哈希这些类标识符值）。

覆盖**仅在**每个目录的顶层匹配；更深层嵌套的版本子目录（例如 `alerts/foo/25.4.1.0/`）不会被当作 `alerts/foo/` 的嵌套覆盖。