# 隐私——bundle会暴露什么、什么可以离开本机

诊断bundle是**关于他人生产系统的证据**。collector从不读取客户的*行数据*，但有多张`system`表会以自由文本描述客户数据。在所有者另有指示之前，把解压后的bundle及由其派生的一切都视为机密。

## 即使经过sanitisation（脱敏）后bundle中仍有什么（cloud / onprem模式）

| 标识符类别 | 出现位置 |
|---|---|
| 主机名、IP、端口、集群拓扑、replica/shard宏 | `system.clusters`、`system.replication_queue.replica_name`、`configuration/`（`<remote_servers>`、`<zookeeper>`、`<macros>`、`<interserver_http_host>`）、`host_info.os.hostname`、`logs/` |
| 数据库、表、列、字典、磁盘和存储策略名称 | 几乎所有文件；`system.tables.create_table_query`包含完整DDL，含注释和默认值 |
| SQL文本（每个查询模式500字符）以及`query_analysis/`中的完整SQL | `system.query_log_details_7_days.query`、`query_details`、`failed_queries.sample_query`、`executions_timeline` |
| 异常消息——常内嵌字面量、路径、主机名 | `query_log_details.exception`、`system.errors.last_error_message`、`replication_queue.last_exception`、`dictionaries.last_exception`、`part_log.exception` |
| 服务器日志行（原始查询、表名、路径、客户端地址） | `system.text_log`、`text_log_<ts>`、`logs/*.log` |
| 进程命令行、挂载点、内核banner | `host_info.top_processes_by_rss`、`host_info.disks`、`host_info.os` |
| 用户名和客户端地址 | `system.processes`、`query_log_details.user`、`system.clusters.user` |
| 堆栈跟踪（函数名、build id） | `system.stack_trace`、`system.crash_log` |

sanitisation **确实会**从`configuration/`中移除：密码、哈希、token、密钥、secret、PEM块、长hex/base64数据块、URL中的凭据。它**不会**移除标识符。假设任何配置文件都会立即暴露组织身份。

gov模式会对数据库/表/用户/主机名做哈希，并隐去文本、日志、host facts（主机信息）和配置。当bundle必须跨越组织边界时，这是正确的选择。

## 对助手的规则

1. **分析留在本地。** 从磁盘读取bundle。不要上传它，不要把大段摘录粘贴到Web服务，也不要把它发送给你当前正在运行之外的任何API。绝不要用bundle内容运行`curl`/`wget`。
2. **最小化引用。** 在findings中，对承载证据的少数几行（3–10行）引用`file → column → value`，查询文本截断到150–300字符，绝不粘贴整个文件或`configuration/` XML。
3. **绝不逐字粘贴** `configuration/`、`logs/`、`host_info.json`、`system.stack_trace`、`system.crash_log.trace_full`或`create_table_query`块到任何会离开本机的内容中（会被分享的聊天记录、工单、pastebin）。先总结，或先脱敏。
4. **gov产物：** `*_gov_name_mapping.csv`和salt绝不能离开客户的机器。绝不要索取salt。如果用户提供映射CSV，只在本地用它标注findings。
5. **分享前先脱敏。** 当用户想把摘要发给别人（支持工单、同事、论坛）时，执行下面的检查清单，并把标识符替换为占位符（`<db>`、`<table>`、`<host-1>`、`<user>`）。
6. **不要跨不同组织的bundle做关联。** 每份bundle都要单独分析。
7. **不要编造标识符。** 如果名称被哈希或隐去，就照实说明——绝不要从哈希或查询片段猜测表名。

## 脱敏检查清单（任何文本离开本机之前）

在草稿中搜索并替换：

- 主机名 / FQDN / IP / 端口：`[a-z0-9-]+\.[a-z0-9.-]+`、`\b\d{1,3}(\.\d{1,3}){3}\b`
- 不属于`system.*`的数据库/表/列名
- 用户名（`default`可以；其他都不行）
- 超出解释finding所需形态的查询文本（保留`SELECT ... FROM <table> WHERE <col> = ? GROUP BY ...`这类形态；去掉字面量）
- 含字面量、路径或主机名的异常消息
- `/var/lib/clickhouse`下的路径、挂载点、设备名
- `configuration/`中除设置名和数值以外的任何内容
- bundle自身的时间戳可以保留；组织名称（常出现在数据库名或`os.hostname`中）不行

## 可以原样分享的内容

- 健康scorecard（区域 → 状态 → 带数字的一行证据）
- 设置名及其值（`parts_to_throw_insert = 3000`、`max_memory_usage = 10 GiB`）
- 错误码、名称和计数
- part（数据分片）数量、大小、时长、速率、百分比
- ClickHouse版本，以及你引用的公开changelog/issue链接
- 以设置、模式和公开文档表述的建议

## 当用户是ClickHouse支持人员时

同样适用这些规则；bundle所有者的同意只覆盖ClickHouse支持系统。不要将bundle内容复制到公开GitHub issue、社区Slack或公司边界之外的LLM服务中。