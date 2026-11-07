# 告警

*从主 [README](../README.md) 移出，该文件保留每节的简短版本。*

## 告警

alert rule（告警规则）是 `alerts/` 中的纯 YAML 文件（可用 `-alerts-dir` 覆盖）。每个规则指定一个 **check**（`check: go:<name>`，即 `internal/alert/checks_*.go` 中的一个 Go 函数），在 collector（采集器）已写出的文件上求值——规则**不会发出自己的查询**，因此在状况不佳的服务器上运行只会付出采集成本，别无其他。如果该 check 返回任何行，告警即触发，这些行会展示在 dashboard（仪表盘）中。规则的 `query:` 是同一测试以只读 SELECT 形式的写法：它是 dashboard 交给客户现场运行的查询，而 check 与之对应（按小时聚合日志桶而非原始行——某个窗口最旧的一小时可能往前多覆盖一小时）。所有告警 SQL 都会校验：只接受 `SELECT` / `WITH`，其他任何语句（`INSERT`、`ALTER`、`DROP` 等）都会使规则失效。

没有 `check:` 的规则仍会像以前一样在服务器上运行其 `query:`——这是编写 check 之前试用新规则的方式。当使用 `-output-format native` 或 `tsv` 时，所有规则都这样运行：这些 check 读取的是 JSONEachRow 文件。

### 规则结果

每次运行都会报告四种不同的结果，因为对阅读归档的人来说，每种结果含义各不相同：

| 结果 | 含义 |
|---|---|
| **fired** | 规则运行并匹配到行——一个真实的 finding（发现）。 |
| **clean** | 规则运行且未匹配到任何内容。 |
| **errored** | 规则无法运行（SQL 错误、该版本缺少某列、没有 `SELECT` grant（授权））。**不是 finding**——单独计数和展示，且不计入“已检查”。 |
| **not applicable**（skipped，不适用） | check 要读取的某个文件未被采集（在 essential 运行中被跳过、没有 Keeper 会话、超出时间预算，或采集失败——会指明对应的 collector 与原因），或此处不存在该系统表——健康的自管理实例上的 `crash_log`，或被配置禁用的 `text_log`/`query_log`——或规则声明了 `modes:` 而本次运行处于其他模式（需要 cloud fan-out 的按 replica 比较、SharedMergeTree 上的 replication queue 规则）。不计入“已检查”，因此绝不会被读成一项通过的检查；原因会按规则记录。 |

```
[alerts] 已评估 8 条规则：1 fired，2 errored，1 skipped（不适用）
```

只有真正给出答案的规则才算*已检查*。dashboard 也与此一致：finding 获得严重级别徽章，errored 的规则获得一个弱化的 **⚠ N Could not run** 标记（绝不是红色的严重级别计数），跳过的规则则以“not applicable”列出并附原因（表不存在，或规则的 `modes:` 排除了本次运行模式）。

缺少**列**始终是错误，绝不是“not applicable”——这正是规则需要[版本门控](versions.md#版本特定查询)的信号。

### `alerts_summary.json`

HTML dashboard 是记录告警结果的唯一其他地方，因此只要它**没有**生成，归档中原本就不会留下任何规则运行过的痕迹。这种情况下，工具改为将 `alerts_summary.json` 写入输出目录。出现条件：

- `-mode gov`（dashboard 被[隐去](modes.md#gov-模式不产出什么)），
- 传入了 `-skip-dashboard`，或
- dashboard 生成失败。

它会记录每条规则的名称、标题、严重级别、文件（包括版本覆盖目录，例如 `24.1.1.0/…`）、结果（`fired` / `clean` / `error` / `skipped`）和**实例数**，以及运行总计。**任何模式下都不会包含匹配到的行**——它们的列按规则定义，因此常常带有数据库名和表名。计数是替代品：*“`too_many_parts` fired，12 个实例”*。

> **Cloud 注意事项。** 在 `cloud` 模式下，按 replica 的表通过 `clusterAllReplicas` 读取，因此 `UNKNOWN_TABLE` 可能表示*在所有地方都不存在*或*仅部分 replica 上存在*——而 `system.crash_log` 只存在于崩溃过的节点上。因此工具会先统计有多少 replica 上确实有该表，再做判断：零表示确实不适用，其他任何情况（或无法验证的结果）都会暴露该错误，而不是冒险掩盖一次真实崩溃。

### YAML schema

```yaml
name: mutation_running_too_long      # 唯一的 snake_case 标识
title: "Mutation running > 3h"       # 人类可读的标题
severity: warning                    # critical | warning | info  （默认 warning）
description: |
  多行说明，解释该告警的含义及应对方法。
tags:
  - mutations
  - performance
check: go:mutation_running_too_long # 在采集文件上求值的 Go check
source:                              # 它读取的 collector 文件（文档用途；有测试
  - system.mutations                 # 要求其中每一个都是随包发布的 collector）
  - system.version
# modes: [cloud]                     # 可选：仅在这些模式下运行（cloud | onprem | gov）；
                                     # 其他模式下该规则报告为 not applicable，而非 errored——
                                     # 适用于只有配合 clusterAllReplicas
                                     # fan-out 才有意义的查询，或 SharedMergeTree 缺少的 replication queue

query: |
  SELECT database, table, mutation_id,
         dateDiff('hour', create_time, now()) AS hours_running,
         parts_to_do
  FROM {sys.mutations}
  WHERE parts_to_do > 0
    AND dateDiff('hour', create_time, now()) > 3
  ORDER BY hours_running DESC

message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h"

# investigate:                       # 可选：供客户自行运行的后续查询
#   - title: "Mutations still running, per table"     # 规则触发时用于聚类——
#     query: |                       # finding 提出的“哪一个？”。只展示，工具不运行；
#       SELECT database, table, count() FROM {sys.mutations}   # 只读 SELECT，{sys.*} 的展开
#       WHERE parts_to_do > 0 GROUP BY database, table          # 与 query 相同。
```

在 `dashboard.html` 中，每条 fired 的告警、或其 check 失败的告警，都会在其实例下方带一行可折叠的 **可在你的集群上运行的查询**。展开后会显示规则的 `query`——`{sys.*}` 已按 bundle 的模式展开，因此可直接粘贴到同一集群上的 `clickhouse-client`——以及规则的 `investigate:` 后续查询，每项都带复制按钮。没有发现任何内容的规则不带 SQL，`alerts_summary.json` 也从不带 SQL（它只有计数）。在 cloud bundle 上，查询会保持 `clusterAllReplicas(…)` 形式，这需要与采集时相同的 `REMOTE` grant。

### `{sys.<table>}` 占位符

在查询中使用 `{sys.<table>}`——求值器会根据运行模式重写它：

| 模式 | `{sys.query_log}` 展开为 |
|---|---|
| `onprem`、`gov` | `system.query_log` |
| `cloud` | `clusterAllReplicas(default, system.query_log)` |

这正是同一条告警能同时适用于单节点和 Cloud 部署的原因。行在 replica 之间共享的表（`parts`、`tables`、`mutations`、`replicas`、`replication_queue`、`detached_parts`、`columns`、`databases`）即使在 cloud 模式下也**不会**被包裹——`clusterAllReplicas` 会重复它们的行（见 `internal/query/template.go`）。

### 消息模板

在 `message:` 中，`{column_name}` 会替换为每个结果行的值。每行生成一条格式化消息，因此返回 5 行的告警会在 dashboard 中产生 5 条消息。

### 新增告警

1. 按上述 schema 在 `alerts/` 中放入新的 `.yaml` 文件。
2. 通过 `{sys.<table>}` 引用系统表，而不是硬编码名称。
3. 保持查询严格只读——安全校验器会阻止其他任何内容。
4. 用 `-skip-archive -skip-config` 运行工具以获得快速反馈循环；没有 `check:` 时查询会现场运行。
5. 编写 check：一个 `func(*bundle.Bundle, mode string) ([]map[string]interface{}, error)`，在 `internal/alert/checks_*.go` 中用 `RegisterCheck` 注册，返回 `message:` 使用的那些列。如果它需要的列尚未采集，将其加入 collector。为每个阈值刚超过和刚好未达到的情况添加 fixture 测试（`internal/alert/checks_fixture_test.go`），然后设置 `check:` 和 `source:`。

### 随包附带的告警规则

仓库在 `alerts/` 中附带 18 条 alert rule（外加 `alerts/<version>/` 中的版本门控覆盖，以及一条仅存在于其中的规则——`keeper_xid_renewal_due`，24.3+）。它们旨在作为起点——请根据你的工作负载调整阈值。

| 规则 | 严重级别 | 触发条件 |
|---|---|---|
| `crash_log_entries` | critical | `system.crash_log` 非空（服务器至少崩溃过一次） |
| `replica_readonly` | critical | 某个复制表处于只读模式（丢失 Keeper 会话、磁盘已满、网络分区） |
| `replication_queue_errors` | critical | replication queue 条目的 `last_exception` 非空 |
| `disk_space_low` | critical | **存放用户数据**的磁盘可用空间不足 15%——在 cloud 模式下**任一 replica** 均如此，消息中会指出上报该情况的主机。只有当某个用户表的存储策略引用了某块磁盘时，该磁盘才被计入（`system.storage_policies` × `system.tables.storage_policy`）；没有任何表引用的磁盘不算数据容量——在托管服务上它是本地文件系统缓存，容量本就设计为会被填满，否则会在健康服务可用空间为 14.7% 时触发此规则。该过滤是证据而非模式检查，因此以 cloud 模式采集的自管理集群仍会因高负载的本地数据盘而抛出 critical。没有带策略的用户表 = 所有磁盘都参与判断 |
| `keeper_health` | critical | 对 7 天内每小时做双信号 Keeper 健康测试：`ZooKeeperHardwareExceptions` 超过 1000 **且** `ZooKeeperTransactions` 低于 7 天中位数的 50%——Keeper 实际不可用（仅流量低永远不会触发：空闲的一小时不是故障）；可捕获从未进入 `query_log` 的故障 |
| `keeper_connection_blips` | warning | 一小时内 `ZooKeeperHardwareExceptions` 超过 1000，同时 Keeper 流量保持在不低于通常水平的 50%（7 天中位数为正）——会话丢失后重新建立 |
| `keeper_exception_spike` | warning | 最近 24 小时的某一小时内 KEEPER_EXCEPTION（code 999）错误超过 20 个（每小时一个实例） |
| `high_exception_rate` | warning | 最近 24 小时的某一小时内，单个异常码的查询异常超过 50 次（每个小时与异常码一个实例，取最差的 24 个） |
| `background_operation_failures` | warning | 最近 24 小时的某一小时内，同一错误码的 merge / fetch / mutation 失败超过 50 次（`part_log`） |
| `merges_stalled` | warning | 最近 24 小时的某一小时内有超过 100 个 `NewPart` 事件且零个完成的 merge（`part_log`）——Keeper 宕机、pool 暂停或每个 merge 都失败 |
| `fetch_pool_saturated` | warning | 按 replica 统计，最近 24 小时中有 3 个小时 `BackgroundFetchesPoolTask` 的每小时平均值达到 `background_fetches_pool_size` 的 90% 及以上——fetch 执行器没有队列，因此在 SharedMergeTree 上这表示 parts 传播滞后 |
| `schedule_pool_saturated` | warning | 按 replica 统计，最近 24 小时中有 3 个小时 `BackgroundSchedulePoolTask` 的每小时平均值达到 `background_schedule_pool_size` 的 95% 及以上——每张表的周期任务都在这里运行；数万张复制表会最先把它占满 |
| `parts_propagation_lag` | warning | *（仅 cloud——`modes: [cloud]`）* 对 SharedMergeTree 表，某个 replica 在某个分区上落后最新 replica 至少 32 个 block，或在最近一天被写入的某分区上完全没有数据——parts 传播滞后；每个 replica 一个实例，附带其最差分区，完整列表见 `system.parts_max_block_by_replica` |
| `too_many_simultaneous_queries` | warning | 最近一小时 code-202（`TOO_MANY_SIMULTANEOUS_QUERIES`）错误超过 10 次（触发 `max_concurrent_queries`） |
| `too_many_parts` | warning | 某个分区的活跃 part 超过 300 个（从 `parts_to_delay_insert` = 1000 起插入被延迟，达到 `parts_to_throw_insert` = 3000 时以 code 252 `TOO_MANY_PARTS` 拒绝插入） |
| `large_parts` | warning | 单个活跃 part 大于 150 GB |
| `mutation_running_too_long` | warning | 某个 mutation 已运行超过 3 小时 |
| `detached_parts_exist` | info | `detached/` 目录中存在 part（失败的 merge、手动 detach、复制冲突） |
| `keeper_xid_renewal_due` | info | *（仅 24.3+）* 按主机实测的使用速率，某个 Keeper 会话将在 24 小时内达到 **32 位**计数器上限（2³¹），前提是该主机有一个活跃 Keeper 连接且 `metric_log` 至少有 2 小时数据支撑估算。报告的是余量，而不是一定会发生的续期：若在 `<zookeeper>` 中启用 `use_xid_64`，计数器为 64 位且永不回绕，而没有系统表暴露该设置——请检查 `configuration/`。在默认 32 位计数器下，续期表现为短暂的 Keeper 异常突发和一次重连；仅当那几分钟内 INSERT 失败时才需调查（`query_log`，或 `asynchronous_insert_log` 中的 flush） |

每条规则都是对采集文件执行的 Go check，测试与 `query:` 中的 `SELECT` 相同；返回的行会成为 dashboard 中的告警实例。在 cloud 模式下，Keeper 规则读取按主机的 `metric_log_by_host_3_days`，因此那里的“通常”中位数跨 3 天，而非 7 天。读取日志表的规则回看 **24 小时或 7 天，按小时**，而不只是最近一小时——bundle 通常在恢复后才采集，只看一小时的规则会对它本该暴露的故障视而不见。直接打开 YAML 文件查看确切阈值并调整。