# 运行`clickhouse-diagnostic`——速查表和重新采集配方

完整参考是仓库的`README.md`及其`docs/`页面：`running.md`（grant（授权）、flag、示例、*Essential or full collection*、*Collection window*、dry-run（试运行））、`modes.md`（模式、gov模式）、`bundle.md`（host facts（主机信息）、日志、配置、输出）、`alerts.md`、`query-analysis.md`、`versions.md`。本页是助手告诉用户如何（重新）采集正确数据所需的子集。

## 1. 构建或获取二进制文件

```bash
make build                                  # → ./bin/clickhouse-diagnostic（Go ≥ 1.23）
make release                                # ./bin/ 中的 linux-amd64 / darwin-amd64 / darwin-arm64 / windows-amd64
```
分发二进制文件时**要连同**`queries.cloud/ queries.onprem/ queries.gov/ alerts/ queries.query_analysis/`一起提供，并从包含它们的目录运行——查询文件夹是相对于**当前工作目录**解析的，而不是相对于二进制文件。否则会出现：`Error: Queries folder './queries.<mode>' does not exist`。

## 2. grant（只读用户）

```sql
CREATE USER sys_read_only IDENTIFIED WITH sha256_password BY '<password>';
GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO sys_read_only;
GRANT SELECT ON system.* TO sys_read_only;
-- SHOW COLUMNS 很重要：没有它，system.columns 对用户数据库会返回零行，
-- 而其他每个文件看起来都正常。绝不要用
-- GRANT SELECT ON <db>.* 代替——它产出的 bundle 一样，但会让诊断
-- 用户读到客户数据。
-- 仅 cloud 模式（clusterAllReplicas fan-out）：
GRANT REMOTE ON *.* TO sys_read_only;
GRANT CREATE TEMPORARY TABLE ON *.* TO sys_read_only;
```
缺少`SHOW`grant**不会**报错——它会悄悄把`system.tables`/`columns`/`databases`缩小到该用户能看到的内容。`system.tables`只包含`system.*`的bundle（诊断包）就是线索。cloud模式下缺少`REMOTE`/`CREATE TEMPORARY TABLE`会以497大声失败。

配置、host facts和日志采集读取的是**本地文件系统**（`/etc/clickhouse-server`、`/var/log/clickhouse-server`、`/proc`）——它们需要操作系统的读权限，而不是grant，并且只有在工具**在**服务器上运行时才有意义。

## 2a. 从agent或脚本传入密码（运行前先读）

CLI依次从`-password <value>`、环境变量`CLICKHOUSE_PASSWORD`、交互式提示（`Enter Password:`，从TTY读取）获取密码。优先使用变量：它不会出现在`ps`里，运行时会打印`Using the password from CLICKHOUSE_PASSWORD`。在非交互式shell中有两件事会出错：

- **提示无法被回答。**agent的shell没有TTY，因此提示读到空字符串，运行失败并报`401 … Code: 194 … Authentication failed (REQUIRED_PASSWORD)`。如果你在输出中看到`Enter Password:`，说明flag从未携带值。
- **内联环境变量赋值不起作用。**`CH_PASS='x' ./clickhouse-diagnostic … -password "$CH_PASS"`会在赋值生效*之前*展开`$CH_PASS`，因此工具收到的是`-password ""`。这是shell规则，不是工具的bug。`CLICKHOUSE_PASSWORD='x' ./clickhouse-diagnostic …`*不带*`-password`时是有效的：工具会自己读取该变量。

改为这样做：

```bash
# 1. 环境变量，不用 flag（密码不会出现在 ps 中）
export CLICKHOUSE_PASSWORD='<password>'
./clickhouse-diagnostic -mode cloud -host <svc> -port 8443 -protocol https -user default -skip-config

# 2. 或从仓库外的文件读取，权限 0600，用完删除
umask 077; printf '%s' '<password>' > ~/.chdiag_pass
./clickhouse-diagnostic … -password "$(cat ~/.chdiag_pass)"
rm -f ~/.chdiag_pass

# 3. 或让人来交互式运行（提示会隐藏输入）
./clickhouse-diagnostic -mode cloud -host <svc> -port 8443 -protocol https -user default -skip-config
```

给助手的规则：绝不把密码写进仓库内的文件（即使是被gitignore的——它仍可能被随包发出或被粘贴）；绝不在总结里回显它；该值会留在本会话的shell历史里，因此要告诉用户采集完成后轮换它；当用户在聊天里给出密码时，使用第1种方式并说明你用了它。

## 3. 模式

| `-mode` | 读取内容 | 备注 |
|---|---|---|
| `onprem`（默认） | 所连接节点的`system.*` | 默认**开启**host facts + 日志（`-host-info/-logs auto`） |
| `cloud` | 对每个replica（副本）的表使用`clusterAllReplicas(default, system.*)` | 默认关闭host facts/日志（它们会描述你的笔记本）；使用`-skip-config` |
| `gov` | database/table/user/host名称哈希后的`system.*`（`SHA256(name \|\| salt)`） | 需要`-salt`（8–64个字母数字，注意保密）；无dashboard（仪表盘）、无查询文本、无配置/host/日志/查询分析；写出`alerts_summary.json`和一个**仅本地**的映射CSV |

**自托管SharedMergeTree集群**（`system.settings`中`cloud_mode = 1`、`Shared*MergeTree`引擎、`s3_with_keeper`磁盘）：每个replica都保留自己的`system.*`表。自v0.7起，发现`cloud_mode = 1`的`-mode onprem`运行会**自行切换到cloud采集**——每个per-replica系统表（`query_log`、`part_log`、`errors`、`metric_log`、`text_log`……）通过`clusterAllReplicas`在`default`集群上分发采集，而共享表（`parts`、`tables`、`columns`、`databases`、`replicas`、`replication_queue`、`mutations`、`detached_parts`）像任何cloud采集一样从一个replica读取——因此按replica比较part（数据分片）仍然需要一个专门的collector（采集器）——同时仍从运行工具的机器上采集host facts、配置和日志文件——只有在工具运行于该节点上时，这些才是该节点的；使用远程`-host`时，它们是collector机器的，头部会带一行`warnings:`说明这一点；`execution_log.txt`记录`collection: onprem → cloud (SharedMergeTree detected)`。该切换需要`REMOTE` + `CREATE TEMPORARY TABLE ON *.*`；没有它们，运行会停留在单节点上，并在头部说明。`-single-node`故意保持旧的单节点行为。较旧的bundle（模式`onprem`，无`collection:`行）描述的是**N个节点中的一个**。

**在节点上采集，在重启它之前采集，而且——在自管理集群上——绝不通过负载均衡器采集。**头部的`node:`行是应答服务器的`hostName()`。工具在这里打印两个独立的finding（发现）。当三个探测返回了不同的名称（负载均衡器——此时每个系统表都来自任意一个replica）时它会警告；另外，当`-host`是远程服务器而host facts和日志是从运行工具的机器读取（一个bundle里有两个主机）时，它也会单独警告。

**负载均衡端点在ClickHouse Cloud上是常态**，不是错误：没有可优先选择的per-replica端点，而cloud采集正是为此构建的——per-replica表会分发采集，只有共享表来自应答的那个replica。因此，只要bundle是这样采集的（包括切换到cloud采集的`-mode onprem`运行），第一个finding就打印为`Note:`，否则打印为`Warning:`；前缀就是要读的东西。混合主机警告不受模式影响——这种混合模式确实会从collector机器读取host facts、配置和日志。采集前刚重启过的节点看起来比集群实际更健康：pre-pass会标记`uptime`低于24小时的情况。

## 4. 常见调用

```bash
# 本地 on-prem 节点，采集全部内容，默认 7 天窗口
./clickhouse-diagnostic -mode onprem -host localhost -port 8123 -user sys_read_only

# ClickHouse Cloud
./clickhouse-diagnostic -mode cloud -host <svc>.<region>.aws.clickhouse.cloud -port 8443 -protocol https \
  -user sys_read_only -skip-config

# gov / 哈希标识符
./clickhouse-diagnostic -mode gov -host gov-ch-01 -salt <YourPrivateSalt>

# 非交互式（CI / agent）：在前一条语句中导出变量——见 §2a
export CH_HOST=… CH_USER=… CH_PASS=…
./clickhouse-diagnostic -mode onprem -host "$CH_HOST" -user "$CH_USER" -password "$CH_PASS" -skip-config

# 安全审查：打印它将运行的每条查询 + EXPLAIN ESTIMATE，不采集任何内容
./clickhouse-diagnostic -mode onprem -host ch-01 -dry-run
```

读取Keeper的collector（`system.distributed_ddl_queue`、`system.zookeeper_connection`）或读取超大日志表的collector（`system.zookeeper_log`）受`LIMIT`和下面的超时约束；在Keeper不健康时，它们可能就是超时的那几个（code 159），这本身就是证据。

有用的flag：`-query-timeout`（默认240秒——服务器以`max_execution_time`强制执行，因此超时的collector查询会在客户的`query_log`中显示为干净的`Code: 159`；`0`表示禁用）、`-output-dir`（默认`./clickhouse_results`）、`-output-format jsonl|native|tsv`（**保持`jsonl`**——本技能和`inspect_bundle.py`只能读`.jsonl`，`native`/`tsv` bundle会被拒绝；见bundle-layout §1）、`-skip-alerts`、`-skip-dashboard`、`-skip-archive`、`-alerts-dir`、`-config-dir`、`-logs-dir`、`-logs-max-mb`（默认50）、`-logs-include-archives`、`-host-info on|off|auto`、`-logs on|off|auto`。

## 4a. Essential或full（`-collect auto|essential|full`）

默认`auto`：一次一秒的负载探测会选择**full**，在吃力的服务器上选择**essential**（`SELECT 1`慢、接近`max_concurrent_queries`或内存限制、负载超过核心数2倍、只读replica）；full运行在查询超时或连续两个查询耗时 > 30秒时也会在运行中切换到essential。essential = 仅`-- tier: essential`的collector、1天窗口、每查询30秒、3分钟预算（`-time-budget`）、`max_threads = 2` / 1 GiB / 低优先级、无每表告警或面板；没有Keeper会话时，等待Keeper的查询会被跳过。bundle在`execution_log.txt`（`collect:`）、dashboard横幅和pre-pass中说明是哪一种。当客户报告工具"耗时太长"或"无法在出问题的节点上运行"时，`auto`就是为此而设；在你已知吃力的节点上强制使用`-collect essential`，等它恢复健康后，再请求`-collect full`以获取parts、tables、detached parts和3–7天历史。

## 5. 时间窗口

采集窗口按查询而定：`query_log`、`metric_log`（固定列）、`asynchronous_insert_log`、`blob_storage_log`、`error_log`、`distributed_ddl_queue`为7天；`part_log`、`query_views_log`和`metric_log_coordination`（较宽或高数据量的那些）为**3天**；`system.text_log`（上限2000行）、text_log直方图和Keeper标记，以及`zookeeper_log`错误为**1天**。`-from`/`-to`（RFC3339或`YYYY-MM-DD`，UTC）会一次性覆盖**每一个**窗口；alert rule（告警规则）保留自己的回看范围，基于已采集的文件衡量（从v0.9起它们不再发送查询），因此有`-to`时它结束于`-to`。

```bash
# 正好是事件窗口（最省、最聚焦）
./clickhouse-diagnostic -mode onprem -host ch-01 -from 2026-08-14T09:00:00Z -to 2026-08-14T13:00:00Z
# 放宽到 30 天
./clickhouse-diagnostic -mode onprem -host ch-01 -from 2026-07-21
```

## 6. 更深的切片

```bash
# system.text_log 的有界切片（需要两个边界；gov 中不可用）
./clickhouse-diagnostic -mode onprem -host ch-01 -collect-text-log \
  -from 2026-08-20T14:00:00Z -to 2026-08-20T15:00:00Z -text-log-level Warning -text-log-limit 200000

# 针对单个 query_id 的查询分析（窗口自动以其 event_time 为中心）
./clickhouse-diagnostic -mode onprem -host ch-01 --query-id 1bc3abaf-968f-4d4f-be3d-f77251b1ff0b

# 针对某个模式的查询分析（normalized_query_hash 来自 bundle 的 query_log_details），最近 7 天
./clickhouse-diagnostic -mode onprem -host ch-01 --normalized-query-hash 7769688026807387533 -from 2026-05-23 -to 2026-05-30
```
查询分析会把12个文件写到`<backup>/query_analysis/`，并增加一个dashboard区段；在gov模式下会被拒绝。

## 7. 输出

```
clickhouse_results/clickhouse_diagnostic_YYYYMMDD_HHMMSS/   # 被归档的文件夹
clickhouse_results/clickhouse_diagnostic_YYYYMMDD_HHMMSS/execution_log.txt   # 运行了什么、失败了什么、花了多少
clickhouse_results/clickhouse_diagnostic_YYYYMMDD_HHMMSS.tar.gz   # 在 -output-dir 中——发送这个
clickhouse_results/clickhouse_diagnostic_<ts>_gov_name_mapping.csv   # 仅 gov——绝不发送
```
分享之前：打开`configuration/`并确认没有敏感内容残留（sanitisation（脱敏）去除的是凭据，而不是主机名/拓扑）；在gov模式下确认salt和CSV不在压缩包内。

## 8. 按finding对应的重新采集配方

| finding | 用以下方式重跑 |
|---|---|
| 事件在已采集窗口之外 | 在事件前后使用`-from <start> -to <end>`（按小时，而不是按天） |
| 需要解释慢/失败的模式 | `--normalized-query-hash <hash>`（来自`query_log_details`）或`--query-id <uuid>`（来自应用/日志） |
| `text_log`切片太短（2000行 = 几分钟） | `-collect-text-log -from … -to … -text-log-level Warning` |
| 日志被截断（`TRUNCATED`头部）或需要轮转文件 | `-logs-max-mb 500 -logs-include-archives` |
| `host_info.json`缺失/降级（工具远程运行） | **在**服务器上运行工具，或在那里使用`-host-info on` |
| 配置缺失 | `-config-dir /etc/clickhouse-server`（指向包含`config.xml`、`config.d/`、`users.d/`的目录） |
| bundle只显示`system`表，或用户数据库的`system.columns`为空 | 授予`SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.*`并重跑（当用户看不到任何数据库时，工具现在会在采集前警告） |
| cloud：部分表以497报错 | 授予`REMOTE` + `CREATE TEMPORARY TABLE`，重跑 |
| gov bundle缺少分析所需内容 | 如果策略允许，采集一个`onprem` bundle并只分享摘要；否则在本地使用映射CSV |
| 跟踪趋势（part增长、错误率） | 数小时/数天后采集第二个bundle，对比`system.parts`计数和`system.errors`值 |
| 多replica / SharedMergeTree集群上的Keeper事件 | 用`-mode cloud`获取所有replica的`metric_log_coordination`、`zookeeper_connection`、`metrics`（每个replica的`MetadataFromKeeperCacheObjects`）、`part_log`；外加`keeper/<host>_<port>.txt`——每个成员的`ruok`/`isro`/`lgif`/`srvr`/`mntr`/`conf`/`dirs`（角色、Raft日志位置、快照大小和传输设置；当工具在服务器上运行时，onprem/gov默认采集，cloud中用`-keeper-mntr on`；`lgif`即使成员不在服务中也会应答），由HC-3.18–3.20和P-61读取——以及Keeper自己的日志文件，工具不采集这些：向曾经是leader的成员索取事件前后的日志行 |
| 同一查询在不同replica上返回不同行数（SharedMergeTree） | 在一个落后的replica **被重启之前**、以`-mode onprem`在**该replica上**采集的bundle（它会自行切换到cloud采集）——pre-pass读取每个主机的后台池饱和度并与实时大小比较（HC-2.13/2.14/2.15），从`system.replicas`读取每个主机落后的表（HC-3.1）、SMT设置和空表占比（HC-2.16），以及头部的节点uptime |
| 调高后台池之后（P-59）：改动生效了吗，集群在追赶吗？ | 几小时后再运行同样的采集，读取`system.metric_log_by_host_3_days`：每个主机上的实时池大小增加（过渡小时），fetch尝试先激增后稳定，`cpu_us`和merge（合并）池回到基线附近；cloud中`system.replicas`的`inserts_in_queue`对热点表在每个主机上为0（用`select_sequential_consistency = 1`复查一个计数） |
| 自管理（ReplicatedMergeTree）集群，而bundle只是N个节点中的一个——故障可能位于没人采集的节点上（某个shard（分片）、某个落后的replica） | 在一个节点上`export CLICKHOUSE_PASSWORD=…`，然后运行`./clickhouse-diagnostic cluster -user <user>`（见`cluster-collect/README.md`）：从`system.clusters`发现主机，并为每个主机采集一个bundle——它运行的节点采集完整内容，其他主机只采集系统表（包括`text_log`、`query_log`）；`-collect-from each-node`会打印同时采集日志文件和配置的按主机命令。按头部的`node:`行并排阅读这些bundle |
| `zookeeper_log_errors_1_day`/`blob_storage_log_7_days`缺失 | 这些表未启用：服务器配置中的`<zookeeper_log>`/`<blob_storage_log>`（两者都有TTL旋钮）；启用它们，等待下一次事件，或者为一个key索取`system.remote_data_paths` |

## 9. bundle故意不包含的内容

客户数据行（从不查询）；`system.zookeeper`（树本身）、`system.remote_data_paths`（每个blob一行——太大）、`system.filesystem_cache`（每个段一行）、`system.trace_log`、`system.processors_profile_log`（查询分析中除外）、`system.users/grants`、`system.backups`、`system.row_policies`、Keeper日志/`mntr`输出，以及服务器未启用时的`system.zookeeper_log`/`system.blob_storage_log`。当某个finding需要其中之一时，列出用户应运行的确切`SELECT`（单个隔离查询，带`LIMIT`），而不是索要一次转储。