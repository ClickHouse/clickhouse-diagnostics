# 采集整个集群

`clickhouse-diagnostic cluster` 一次性在**自管理集群的每台主机**上运行诊断。它先向一个节点询问集群包含哪些主机（`system.clusters`），打印出将为每台主机运行的命令，并在你确认后执行。每台主机得到一份 bundle（诊断包），另附一份说明哪些成功了的摘要。它是 `clickhouse-diagnostic` 二进制的一部分，因此无需额外安装。

## 两种运行方式

| `-collect-from` | 作用 | 得到什么 |
|---|---|---|
| `here`（默认） | 从**本机**逐台主机运行所有采集 | 每台主机的所有系统表，包括 `text_log` 和 `query_log` |
| `each-node` | 只**打印**要在每台主机上运行的命令 | 所有内容，包括服务器日志文件和配置 |

## 限制

- **从一台能访问所有主机** ClickHouse HTTP 端口的机器上运行它（8123，或使用 https 时的 8443）。其中一个节点或 pod 是自然的选择。主机名来自 `system.clusters`，因此必须在它运行的地方能够解析。
- **`here` 模式不会从其他主机采集服务器日志文件和配置。** 这些内容从本地磁盘读取，所以只有它运行所在的那个节点能拿到。它**确实会**从每台主机采集 `system.text_log` 和 `system.query_log`，这覆盖了大多数排查场景。当你需要那些文件时，使用 `each-node` 并在每台主机上运行打印出的命令。

## 示例

在其中一个节点上，于包含 `clickhouse-diagnostic` 和 `queries.*` 文件夹的目录中执行：

```bash
export CLICKHOUSE_PASSWORD='…'        # 必填：密码永远不会作为 flag 传递
./clickhouse-diagnostic cluster -user diag
```

```
发现 4 台主机（来源 localhost；集群：default, prod_cluster）

  ch-node-1     10.0.0.11   分片 1 副本 1（集群的发现节点）
  ch-node-2     10.0.0.12   分片 1 副本 2
  ch-node-3     10.0.0.13   分片 2 副本 1
  ch-node-4     10.0.0.14   分片 2 副本 2

将逐台主机运行：
./clickhouse-diagnostic -mode onprem -protocol http -host localhost -port 8123 -user diag -single-node ...
...
运行 4 个采集任务？[y/N] y
→ ch-node-1 ... ok，耗时 41 秒，3.1 MB
...
bundle 位于 ./cluster_bundles_20261015_101500
```

要从每台主机获取日志文件和配置，打印每台主机的命令，再各自在对应主机上运行：

```bash
./clickhouse-diagnostic cluster -user diag -collect-from each-node
```

## 选项

| 选项 | 含义 |
|---|---|
| `-host` | 用于发现集群的节点。默认 `localhost`。 |
| `-cluster NAME` | 仅限该集群的主机。默认：`system.clusters` 列出的每台主机，每台机器一次。 |
| `-port`、`-protocol https` | HTTP 端口和协议，每台主机相同。 |
| `-mode gov` | 哈希化标识符。在 `--` 之后传入 salt：`-- -salt <YourPrivateSalt>`。它永远不会被打印，`each-node` 命令会在每台主机上询问它。 |
| `-- <flags>` | 传递给每次运行，例如 `-- -from 2026-10-01 -to 2026-10-02`。该命令自行设置的 flag（`-host`、`-password`、`-output-dir`、`-single-node`、…）会被拒绝。 |
| `-yes`、`-timeout`、`-output-dir` | 跳过确认；每台主机允许的时间（默认 30m）；bundle 的输出位置。 |

## 说明

- 每台主机都需要主 [README → 所需授权](../README.md#所需的-grant授权) 中描述的用户和 grant。
- 每台主机的运行会探测**它自己**的负载，并可能在资源紧张的主机上只采集 essential 表（见 [docs/running.md → essential 与 full 采集](../docs/running.md#essential-或-full-采集-collect)）；每份 bundle 的 `execution_log.txt` 和 dashboard 会说明是哪一种。`-- -collect full` 会强制在每台主机上执行完整采集。
- 主机逐台运行，因此一个慢节点不会和其他节点争抢集群资源。没有产出归档的主机会列在 `summary.txt` 中，旁边是它的 `run.log`，命令会以退出码 1 结束。
- 每份 bundle 各自独立上传，并且每份都必须符合支持门户 200 MB 的限制。摘要会标出任何不符合的 bundle。