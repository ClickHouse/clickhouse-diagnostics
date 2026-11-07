# 阅读配方 — 查询bundle（诊断包）的JSONL

按以下顺序选择工具：

1. **`clickhouse local`**（任何`clickhouse`二进制都自带；`clickhouse local -q "..."`）。对`file('x.jsonl', JSONEachRow)`执行完整SQL，精确的`UInt64`、`formatReadableSize`、`quantile`。最佳选择。
2. **Python 3标准库**（`json`、`collections`）。始终可用；对带引号的64位整数安全。`scripts/inspect_bundle.py`只使用它。
3. **`jq`** — 适合过滤/分组字符串和小整数；**绝不要**用于对带引号的`UInt64`做算术（`tonumber`在超过2^53时会舍入）。

下方约定：`B=<extracted bundle dir>`；文件用glob匹配，因为有时间戳后缀。`clickhouse local`直接接受glob：`file('$B/system.parts_2*.jsonl', JSONEachRow)`。**只要另一个collector（采集器）的名字以当前名字加`_`开头，就把glob写成`<collector>_2*.jsonl`（时间戳以年份开头）** — `system.parts_*`也会匹配`system.parts_max_block_recent_*` / `_by_replica_*`，`system.replicas_*`也会匹配`system.replicas_problem_tables_*` / `system.replicas_answered_*`，`system.text_log_*`也会匹配`_histogram_1_day` / `_keeper_1_day`，错误文件的行会被静默混入。给定schema或进行cast时，带引号的整数会被自动解析：`toUInt64(bytes_on_disk)`。当某列是人类可读字符串（`system.disks.free_space`）时，改用`free_pct`。

纪律（来自分析规则）：只选择你要评论的列；每个结果都加`LIMIT`（原始结果3–10行，聚合结果 ≤ 30行）；查询文本截断到150–300字符；绝不要把整张表粘贴到摘要中。

## 0. 清单与覆盖情况

```bash
A=clickhouse_diagnostic_<ts>.tar.gz                      # 本案例的 bundle（较旧版本：clickhouse_backup_<ts>.tar.gz）
tar -tzf "$A" | head -50                                 # 里面有什么，在解压之前
D=$(mktemp -d /tmp/chdiag.XXXXXX) && tar -xzf "$A" -C "$D"   # 每个 bundle 一个新目录：两个案例绝不共用
B=$(find "$D" -mindepth 1 -maxdepth 1 -type d)           # 它唯一的运行文件夹 — 或预检脚本 "extracted to" 目录里的那个
ls -la "$B"; wc -l "$B"/*.jsonl                          # 0 行的文件是有意义的（见 bundle-layout §1）
cat "$B"/system.version_*.jsonl
head -c 400 "$B"/system.part_log_3_days_*.jsonl          # 在此 bundle 上确认列名
grep -l '^### support-diagnostic: TRUNCATED' "$B"/logs/* 2>/dev/null
```

实际覆盖的采集窗口（query_log是最密集的信号）：
```sql
SELECT min(time), max(time), count() FROM file('$B/system.query_log_details_7_days_*.jsonl', JSONEachRow)
```
Python等价写法：`python3 skills/clickhouse-diagnostic/scripts/inspect_bundle.py "$B"`。

## 1. 告警

来自`dashboard.html`（包含匹配的行）：
```bash
python3 - "$B/dashboard.html" <<'EOF'
import json,re,sys
html=open(sys.argv[1],encoding='utf-8').read()
m=re.search(r'const DATA = (\{.*?\});\s*\n', html, re.S)
data=json.loads(m.group(1))
for a in data['alerts']:
    print(a['name'], a['severity'], 'fired' if a.get('rows') else ('error' if a.get('error') else 'clean'), len(a.get('rows') or []))
EOF
```
来自`alerts_summary.json`：`jq -r '.rules[] | "\(.state)\t\(.severity)\t\(.name)\t\(.instance_count)"' "$B/alerts_summary.json"`。
自行从JSONL重新评估规则：阈值在`health-checks.md`。

## 2. part（数据分片）与merge（合并）

每个分区的part数（告警阈值300，服务器在`parts_to_throw_insert`时抛出，新版本上OSS默认3000 / Cloud 10000 — 检查`system.tables.engine_full`是否有覆盖）：
```sql
SELECT database, table, partition_id, count() AS parts,
       formatReadableSize(sum(toUInt64(bytes_on_disk))) AS size, round(avg(toUInt64(rows))) AS avg_rows
FROM file('$B/system.parts_2*.jsonl', JSONEachRow)
WHERE active = 1 AND database NOT IN ('system','INFORMATION_SCHEMA','information_schema')
GROUP BY 1,2,3 ORDER BY parts DESC LIMIT 10
```
未合并的level-0 part（插入速度超过merge）：
```sql
SELECT database, table, partition_id, countIf(level = 0) AS l0_parts, count() AS parts
FROM file('$B/system.parts_2*.jsonl', JSONEachRow) WHERE active = 1
GROUP BY 1,2,3 HAVING l0_parts > 50 ORDER BY l0_parts DESC LIMIT 10
```
微小的part（批大小问题）：每个表`avg(toUInt64(rows)) < 10000`且`count() > 100`。
Wide与compact：`GROUP BY part_type`。过大的part：`toUInt64(bytes_on_disk) > 150*1024*1024*1024`。

来自`part_log`的merge活动与失败（分桶求和 — 除以`count`）：
```sql
SELECT event_type, merge_reason, sum(toUInt64(count)) AS events,
       round(sum(toUInt64(duration_ms)) / sum(toUInt64(count))) AS avg_ms,
       formatReadableSize(sum(toUInt64(size_in_bytes))) AS bytes
FROM file('$B/system.part_log_3_days_*.jsonl', JSONEachRow)
GROUP BY 1,2 ORDER BY events DESC
```
失败的part操作：`WHERE error != 0` → 按`table_name, event_type`分组的`error, exception (sample), distinct_exceptions, sum(count)`。
插入速率近似：按`time`桶、按`table_name`统计`event_type = 'NewPart'`事件 — 大量NewPart事件且`size_in_bytes/count`很小 = 大量小插入。
TTL工作：`merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')`或`event_type = 'RemovePart'`。

## 3. 复制与Keeper

```sql
SELECT database, table, is_readonly, is_session_expired, absolute_delay, queue_size, inserts_in_queue,
       merges_in_queue, parts_to_check, last_queue_update_exception
FROM file('$B/system.replicas_2*.jsonl', JSONEachRow)
ORDER BY is_readonly DESC, absolute_delay DESC LIMIT 10;
-- Keeper 支撑的列，用于有问题的表加上一个健康样本（≤ 200 张表）
SELECT database, table, is_readonly, active_replicas, total_replicas,
       toInt64(log_max_index) - toInt64(log_pointer) AS log_gap, zookeeper_exception
FROM file('$B/system.replicas_problem_tables_*.jsonl', JSONEachRow)
ORDER BY is_readonly DESC, total_replicas - active_replicas DESC, log_gap DESC LIMIT 20
```
队列形态（任何单个`type` > 60个条目就是某个replica（副本）落后）：
```sql
SELECT type, count(), countIf(last_exception != '') AS with_error, max(num_tries) AS max_tries, min(create_time) AS oldest
FROM file('$B/system.replication_queue_*.jsonl', JSONEachRow) GROUP BY type ORDER BY 2 DESC
```
非空的`zookeeper_exception`点出只读replica背后的Keeper侧失败，`last_queue_update_exception`则是本地队列更新失败；某个仍在排队的类型上`max_tries`很高，意味着该条目在反复重试并失败，而不是在等待轮到自己。两组在gov中都会hash（空仍为空，因此“它到底有没有报错？”这个问题仍然成立）。

Keeper健康测试（HC-3.8）— 每小时两个计数器对照7天中位数，逐小时给出结论：
```sql
WITH (SELECT quantileExactHigh(0.5)(toUInt64(zk_transactions)) FROM file('$B/system.metric_log_7_days_*.jsonl', JSONEachRow)) AS med
SELECT time, toUInt64(zk_hw_exceptions) AS hw, toUInt64(zk_transactions) AS tx, round(100 * tx / greatest(med, 1)) AS pct_of_median,
       multiIf(hw > 1000 AND tx < 0.5 * med, 'UNAVAILABLE', hw > 1000, 'blip', tx < 0.1 * med, 'idle/disconnected', 'ok') AS verdict
FROM file('$B/system.metric_log_7_days_*.jsonl', JSONEachRow)
WHERE verdict != 'ok' ORDER BY time
```
在更丰富的文件上同样处理（列名带有聚合）：在`system.metric_log_coordination_3_days_*.jsonl`中把`zk_hw_exceptions`替换为`"sum(ProfileEvent_ZooKeeperHardwareExceptions)"`，把`zk_transactions`替换为`"sum(ProfileEvent_ZooKeeperTransactions)"`；添加`"max(CurrentMetric_ZooKeeperSession)"`（0 = 该小时没有会话）。

按小时的会话标记（哪一分钟、服务器迁移到了哪个Keeper主机）：
```sql
SELECT toStartOfHour(event_time) AS h,
       countIf(message LIKE '%Session expired%') AS expired, countIf(message LIKE '%Finalizing session%') AS finalized,
       countIf(message LIKE '%Connected to ZooKeeper%') AS connected, countIf(message LIKE '%Trying to establish a new connection%') AS reconnecting,
       anyIf(leftUTF8(message, 160), message LIKE '%Connected to ZooKeeper%') AS example
FROM file('$B/system.text_log_2*.jsonl', JSONEachRow) GROUP BY h HAVING expired + finalized + connected + reconnecting > 0 ORDER BY h
```
（`system.text_log_keeper_1_day_*.jsonl`已为整天预先计算了相同的计数。）每小时的平均Keeper延迟 — “先饱和”信号 — 来自协调文件：`"sum(ProfileEvent_ZooKeeperWaitMicroseconds)" / "sum(ProfileEvent_ZooKeeperTransactions)"`；按错误分类的失败操作来自`system.zookeeper_log_errors_1_day_*.jsonl`（`op_num, error, failed_requests, sessions_affected`）。然后从`query_log_details`取每小时999/319/571（§6），从`part_log`取每小时`error = 999`的`MergeParts`（§2）— 这些小时必须对得上。`system.errors`中的累计999/242只说明“自重启以来”；`system.error_log_7_days`（≥ 24.8，存在时）按小时给出它们，包含后台线程。

### SharedMergeTree part传播滞后（P-59）— 识别、确定修复规模、之后验证

当bundle是cloud采集（或切换过的onprem运行）且`system.settings`中`cloud_mode = 1` / `system.tables`列出`Shared*MergeTree`引擎时适用。三个问题，三个文件。

**1. 谁落后，落后在什么上** — cloud `system.replicas`（v0.7：按主机，有问题的行在前，每主机 ≤ 200行；在SMT上`inserts_in_queue` = 该replica尚未fetch的level-0 part，`absolute_delay` = 最旧那个的年龄）：
```sql
SELECT hostname, count() AS tables_behind, max(absolute_delay) AS max_delay_s, sum(toUInt64(inserts_in_queue)) AS parts_behind,
       argMax(concat(database, '.', table), absolute_delay) AS worst_table
FROM file('$B/system.replicas_2*.jsonl', JSONEachRow)
WHERE absolute_delay > 60 GROUP BY hostname ORDER BY parts_behind DESC;
-- 上限按每主机未过滤的行数判断：恰好 200 = 被截断，该主机的列表是它最差的 200 个，而不是全部
SELECT hostname, count() AS rows FROM file('$B/system.replicas_2*.jsonl', JSONEachRow) GROUP BY hostname HAVING rows = 200
``` 多个主机上出现相同的 `oldest_part_to_get` 是一个 finding，而不是多个。

**2. 是不是池的问题，在哪些主机上** — `system.metric_log_by_host_3_days`，每个主机对照其**实时**大小：
```sql
SELECT hostname,
       countIf(avg_fetch_pool_tasks    >= 0.9  * "max(CurrentMetric_BackgroundFetchesPoolSize)")  AS fetch_hours_pinned,
       countIf(avg_schedule_pool_tasks >= 0.95 * "max(CurrentMetric_BackgroundSchedulePoolSize)") AS schedule_hours_pinned,
       max("max(CurrentMetric_BackgroundFetchesPoolSize)") AS fetch_size, min("max(CurrentMetric_BackgroundFetchesPoolSize)") AS fetch_size_min,
       round(sum("sum(ProfileEvent_SharedMergeTreeDataPartsFetchAttempt)") /
             greatest(sum("sum(ProfileEvent_SharedMergeTreeSelectPartsForRendezvousFetchParts)" + "sum(ProfileEvent_SharedMergeTreeSelectPartsForCoordinatedFetchParts)"), 1), 2) AS attempted_per_selected,
       round(avg("sum(ProfileEvent_SharedMergeTreeDataPartsFetchAttempt)")) AS fetch_attempts_per_h,
       round(sum("sum(ProfileEvent_SharedMergeTreeVirtualPartsUpdatesLeaderSuccessfulElection)") / (3600 * count())) AS elections_per_s,
       round(sum(zk_wait_us) / greatest(sum(zk_transactions), 1) / 1000, 1) AS keeper_ms_per_tx
FROM file('$B/system.metric_log_by_host_3_days_*.jsonl', JSONEachRow)
WHERE time > (SELECT max(time) FROM file('$B/system.metric_log_by_host_3_days_*.jsonl', JSONEachRow)) - INTERVAL 24 HOUR
GROUP BY hostname ORDER BY fetch_hours_pinned DESC, schedule_hours_pinned DESC
```
这样读：某主机上pinned小时数 ≥ 3 = 饱和（HC-2.15）；`fetch_size ≠ fetch_size_min` = 实时大小在窗口内发生了变化（增加与配置重新加载*或*带新配置的重启都吻合 — `metric_log`跨越重启；减少只可能是重启，因为池是`IncreaseOnly`）；`attempted_per_selected` ≪ 1 = 执行器在丢弃工作（没有队列）；`fetch_attempts_per_h`在各主机和各小时都平稳 ≈ 在`size × 3600 / 每次 fetch 的秒数` = 在上限上工作，不是卡住；schedule被钉满而fetch没有 = schedule池在限制fetch；`elections_per_s` ≈ Shared* 表数 / `shared_merge_tree_leader_update_period_seconds`。如果正则列不存在（较旧构建），退回到用`avg_fetch_pool_tasks`对照`server_settings`，以及用每12小时的`part_log` `DownloadPart`估算吞吐。

**3. 需求侧事实** — `system.merge_tree_settings`和`system.tables`：
```sql
SELECT name, value, changed FROM file('$B/system.merge_tree_settings_*.jsonl', JSONEachRow)
WHERE name IN ('shared_merge_tree_parts_load_batch_size', 'shared_merge_tree_leader_update_period_seconds', 'parts_to_throw_insert', 'parts_to_delay_insert');
SELECT count() AS shared_tables, countIf(toUInt64OrZero(toString(total_rows)) = 0) AS empty, countIf(toUInt64OrZero(toString(total_rows)) BETWEEN 1 AND 99) AS under_100
FROM file('$B/system.tables_*.jsonl', JSONEachRow) WHERE engine LIKE 'Shared%'
```

**确定修复规模**（P-59）：`background_fetches_pool_size`和`background_schedule_pool_size`是`IncreaseOnly` — 配置重新加载会在线应用它们；16 → 128和512 → 1024在数万张表的规模上清除了积压；一次在几台主机上铺开，同时观察对象存储延迟和Keeper ms/tx；之后为schedule池按类型的上限计划一次滚动重启。然后是`shared_merge_tree_parts_load_batch_size` 32 → 64、`shared_merge_tree_leader_update_period_seconds` 30 → 300，删除空表，批量插入。对于期间必须一致的读取，使用`select_sequential_consistency = 1`。

**之后验证**（几小时后的第二个bundle）：查询2在每个主机上显示大小*增加*（`fetch_size_min` = 旧值，`fetch_size` = 新值 — 这是转变，不能证明它是如何落地的），`fetch_attempts_per_h`先激增然后稳定在新上限以下；查询1对热点表不返回行（`inserts_in_queue`的下降可能是别处的merge覆盖了这些part — 0才是证据，用`select_sequential_consistency = 1`复核一个计数）；按主机每小时`cpu_us / (3600e6 × cores) × 100`和`avg_merge_pool_tasks`在`max_parts_active`再次下降后回到接近事件前的小时 — 追赶期会合并刚fetch的小part几个小时。

## 3a. Keeper与对象存储文件（为Keeper事件添加）

按小时和错误分类的失败Keeper操作（仅在启用`zookeeper_log`时存在；该文件只保存失败的响应）：
```sql
SELECT time, error, sum(toUInt64(failed_requests)) AS failed, max(toUInt64(sessions_affected)) AS sessions, groupArray(op_num) AS ops
FROM file('$B/system.zookeeper_log_errors_1_day_*.jsonl', JSONEachRow)
WHERE error IN ('ZSESSIONEXPIRED', 'ZCONNECTIONLOSS', 'ZOPERATIONTIMEOUT') GROUP BY time, error ORDER BY time
```
每小时每个错误码，包含后台线程（`error_log`，≥ 24.8）：
```sql
SELECT time, error, sum(toUInt64(errors)) AS n FROM file('$B/system.error_log_7_days_*.jsonl', JSONEachRow)
WHERE code IN (999, 242, 252, 107, 319, 571) GROUP BY time, error ORDER BY time, n DESC
```
从未完成的DDL，以及被重放的DDL（同一条语句出现在许多条目下）：
```sql
SELECT status, count(), min(query_create_time) AS oldest, groupUniqArray(5)(host) AS hosts
FROM file('$B/system.distributed_ddl_queue_*.jsonl', JSONEachRow) WHERE status != 'Finished' GROUP BY status;
SELECT leftUTF8(query, 120) AS q, uniq(entry) AS entries, countIf(toInt32(exception_code) = 57) AS uuid_collisions
FROM file('$B/system.distributed_ddl_queue_*.jsonl', JSONEachRow) GROUP BY q HAVING entries > 1 ORDER BY entries DESC LIMIT 10
```
对象存储：用户数据是否在上面，以及在`FILE_DOESNT_EXIST`那一小时之前上传是否失败/删除是否激增？
```sql
SELECT policy_name, disks FROM file('$B/system.storage_policies_*.jsonl', JSONEachRow);
SELECT name, type FROM file('$B/system.disks_*.jsonl', JSONEachRow);
SELECT time, event_type, sum(toUInt64(count)) AS ops, sumIf(toUInt64(count), toUInt8(failed) = 1) AS failed, anyIf(example_error, toUInt8(failed) = 1) AS err
FROM file('$B/system.blob_storage_log_7_days_*.jsonl', JSONEachRow) GROUP BY time, event_type ORDER BY time
```
按replica的快照倾斜（cloud模式；每个`hostname`一行）— Keeper持有的元数据缓存和当前会话：
```sql
SELECT hostname, anyIf(value, metric = 'MetadataFromKeeperCacheObjects') AS cache_objects, anyIf(value, metric = 'ZooKeeperSession') AS zk_sessions,
       anyIf(value, metric = 'ReadonlyReplica') AS readonly_tables
FROM file('$B/system.metrics_*.jsonl', JSONEachRow) GROUP BY hostname ORDER BY cache_objects
```
服务器uptime以及它隐含的错误率：`SELECT value FROM file('$B/system.asynchronous_metrics_*.jsonl', JSONEachRow) WHERE metric = 'Uptime'` → 用它来除`system.errors.value`和`system.events.value`。仍在进行中的fetch：按`elapsed`排序的`system.replicated_fetches_*.jsonl`。每小时每组件的Warning/Error量：`system.text_log_histogram_1_day_*.jsonl`（`time, level, logger_class, count, example`）。

## 3b. Keeper请求 — `aggregated_zookeeper_log`（≥ 26.3）

来自`system.aggregated_zookeeper_log`的三个文件：**谁**在发送Keeper请求（`_by_hour_2_days`，组件 × 操作）、它们**何时**变慢（`_by_minute_1_day`，cloud中按replica）以及它们发往**哪里**（`_paths_1_day`）。延迟以微秒计，按请求数加权，仅独立请求；`ops`统计独立请求，`in_multi`统计Multi / MultiRead内部的子请求。

谁在给Keeper施压 — 窗口内的组件份额，以及顶层组件的小时形态：
```sql
SELECT component, sum(toUInt64(ops) + toUInt64(in_multi)) AS requests,
       round(100 * requests / sum(requests) OVER (), 1) AS pct,
       round(sum(toFloat64(avg_latency_us) * toUInt64(ops)) / nullIf(sum(toUInt64(ops)), 0) / 1000, 1) AS avg_ms
FROM file('$B/system.aggregated_zookeeper_log_by_hour_2_days_*.jsonl', JSONEachRow)
GROUP BY component ORDER BY requests DESC LIMIT 10;
SELECT time, sum(toUInt64(ops) + toUInt64(in_multi)) AS requests
FROM file('$B/system.aggregated_zookeeper_log_by_hour_2_days_*.jsonl', JSONEachRow)
WHERE component = '<top component>' GROUP BY time ORDER BY time
```
慢操作，以及删除中ZNONODE的份额（P-62）：
```sql
SELECT time, component, operation, toUInt64(ops) AS ops, round(toFloat64(avg_latency_us) / 1e6, 2) AS avg_s
FROM file('$B/system.aggregated_zookeeper_log_by_hour_2_days_*.jsonl', JSONEachRow)
WHERE toUInt64(ops) >= 100 AND toFloat64(avg_latency_us) >= 1e6 ORDER BY avg_s DESC LIMIT 20;
SELECT sum(ops + in_multi) AS removes, sum(errors['ZNONODE']) AS znonode, round(100 * znonode / nullIf(removes, 0), 1) AS pct
FROM file('$B/system.aggregated_zookeeper_log_by_hour_2_days_*.jsonl', JSONEachRow,
          'operation String, ops UInt64, in_multi UInt64, errors Map(String, UInt64)')  -- 给出 schema：否则 map 会被推断为 tuple
-- errors 统计独立请求 AND Multi 内部的子请求，所以分母也如此
WHERE operation IN ('Remove', 'TryRemove', 'RemoveRecursive')
```
何时，以及哪个replica（cloud：`hostname`；onprem/gov文件只有一台服务器，没有hostname列）：
```sql
SELECT hostname, round(quantileExact(0.5)(toFloat64(avg_latency_us)) / 1000, 1) AS median_ms,
       round(max(toFloat64(avg_latency_us)) / 1000, 1) AS worst_ms, sum(toUInt64(loss_failures)) AS loss
FROM file('$B/system.aggregated_zookeeper_log_by_minute_1_day_*.jsonl', JSONEachRow)
WHERE toUInt64(ops) > 0 GROUP BY hostname ORDER BY median_ms DESC
```
在哪里 — 增长和热点路径（gov文件：用`shape`代替`path`）：
```sql
SELECT path, toUInt64(creates) AS creates, toUInt64(removes) AS removes, toInt64(net_creates) AS net, toUInt64(raw_paths) AS raw_paths
FROM file('$B/system.aggregated_zookeeper_log_paths_1_day_*.jsonl', JSONEachRow) ORDER BY net DESC LIMIT 10;
SELECT path, toUInt64(ops) AS ops, round(100 * toUInt64(in_multi) / ops, 1) AS pct_in_multi, toUInt64(raw_paths) AS raw_paths
FROM file('$B/system.aggregated_zookeeper_log_paths_1_day_*.jsonl', JSONEachRow) ORDER BY ops DESC LIMIT 10
```

### 提供给客户的实时查询（其服务器 ≥ 25.9；`is_subrequest`需要 ≥ 26.3）

bundle的窗口是固定的（2天 / 1天 / 1天）；对于更早的事件，或为了确认修复，让客户在他们自己的集群上运行这些查询 — 该表保留30天。替换`<cluster>`（Cloud：`default`）、`<t0>` / `<t1>`（事件窗口）和`<b0>` / `<b1>`（事件之前、长度相同的正常窗口）。只读，针对一张系统表的一天数据；在非常繁忙的服务上把窗口收窄到小时。在25.9–26.2上去掉`is_subrequest`条件 — 此时计数会包含子请求，而Multi延迟会在它们上被重复计算。

1. 每分钟、每个replica的Keeper延迟和失败 — 哪个replica、哪一分钟（P-65、HC-3.21）：
```sql
SELECT toStartOfMinute(event_time) AS minute, hostname,
       sumIf(count, is_subrequest = 0) AS ops,
       round(sumIf(average_latency * count, is_subrequest = 0) / nullIf(ops, 0) / 1000, 1) AS avg_ms,
       round(maxIf(average_latency, is_subrequest = 0) / 1000, 1) AS worst_group_ms,
       sum(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))) AS failed
FROM clusterAllReplicas('<cluster>', system.aggregated_zookeeper_log)
WHERE event_date >= toDate('<t0>') AND event_time >= '<t0>' AND event_time < '<t1>'
GROUP BY minute, hostname ORDER BY minute, hostname
```
2. 事件窗口内相对基线的会话 — 退化的会话：
```sql
SELECT hostname, session_id,
       sumIf(count, event_time >= '<t0>' AND event_time < '<t1>' AND is_subrequest = 0) AS window_ops,
       round(sumIf(average_latency * count, event_time >= '<t0>' AND event_time < '<t1>' AND is_subrequest = 0) / greatest(window_ops, 1) / 1000, 1) AS window_ms,
       sumIf(count, event_time >= '<b0>' AND event_time < '<b1>' AND is_subrequest = 0) AS baseline_ops,
       round(sumIf(average_latency * count, event_time >= '<b0>' AND event_time < '<b1>' AND is_subrequest = 0) / greatest(baseline_ops, 1) / 1000, 2) AS baseline_ms
FROM clusterAllReplicas('<cluster>', system.aggregated_zookeeper_log)
WHERE event_date >= toDate('<b0>') AND event_time >= '<b0>' AND event_time < '<t1>'
GROUP BY hostname, session_id HAVING window_ops > 0 ORDER BY window_ms DESC LIMIT 30
```
3. 哪个组件发送了什么（P-62、P-66–P-68）— 需要 **26.2+**（`component`那时才出现；在25.9–26.1上从SELECT和GROUP BY中去掉`component`以得到按操作的总计，并如上所述去掉`is_subrequest`条件）：
```sql
SELECT component, operation, sumIf(count, is_subrequest = 0) AS ops, sumIf(count, is_subrequest = 1) AS in_multi,
       round(sumIf(average_latency * count, is_subrequest = 0) / nullIf(ops, 0) / 1000, 1) AS avg_ms,
       sumMap(mapFilter((code, n) -> code != 'ZOK', errors)) AS failures
FROM clusterAllReplicas('<cluster>', system.aggregated_zookeeper_log)
WHERE event_date >= toDate('<t0>') AND event_time >= '<t0>' AND event_time < '<t1>'
GROUP BY component, operation ORDER BY ops + in_multi DESC LIMIT 30
```
4. 请求最多、增长最多的路径（P-63、P-64）：
```sql
-- 两遍：一遍处理只会留下两个相邻数字中的第二个（/tables/01/123/ → /tables/N/123/）
SELECT replaceRegexpAll(replaceRegexpAll(parent_path, '/[0-9]+(/|$)', '/N\\1'), '/[0-9]+(/|$)', '/N\\1') AS path, sum(count) AS ops, sumIf(count, is_subrequest = 1) AS in_multi,
       -- 只算成功请求：回答了 ZNODEEXISTS 的 CreateIfNotExists，或回答了 ZNONODE 的 Remove，什么也没改变
       sumIf(greatest(toInt64(count) - toInt64(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))), 0), operation IN ('Create', 'CreateIfNotExists')) AS creates,
       sumIf(greatest(toInt64(count) - toInt64(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))), 0), operation IN ('Remove', 'TryRemove', 'RemoveRecursive')) AS removes,
       toInt64(creates) - toInt64(removes) AS net_creates  -- 请求平衡：一个 RemoveRecursive 会删除一棵子树
FROM clusterAllReplicas('<cluster>', system.aggregated_zookeeper_log)
WHERE event_date >= toDate('<t0>') AND event_time >= '<t0>' AND event_time < '<t1>' AND parent_path != ''
GROUP BY path ORDER BY ops DESC LIMIT 20
```
5. 每个路径的删除数以及有多少没找到可删除的东西（P-62）：
```sql
SELECT parent_path, sum(count) AS removes,
       sum(arraySum(mapValues(mapFilter((code, n) -> code = 'ZNONODE', errors)))) AS znonode  -- map 的键是 Enum：要过滤，不要索引
FROM clusterAllReplicas('<cluster>', system.aggregated_zookeeper_log)
WHERE event_date >= toDate('<t0>') AND event_time >= '<t0>' AND event_time < '<t1>'
  AND operation IN ('Remove', 'TryRemove', 'RemoveRecursive')
GROUP BY parent_path ORDER BY removes DESC LIMIT 20
```
6. 某个子树上每分钟的创建与删除对比 — 增长以及清理停滞的时刻（P-63）：
```sql
SELECT toStartOfMinute(event_time) AS minute,
       sumIf(greatest(toInt64(count) - toInt64(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))), 0), operation IN ('Create', 'CreateIfNotExists')) AS creates,
       sumIf(greatest(toInt64(count) - toInt64(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))), 0), operation IN ('Remove', 'TryRemove', 'RemoveRecursive')) AS removes
FROM clusterAllReplicas('<cluster>', system.aggregated_zookeeper_log)
WHERE event_date >= toDate('<t0>') AND event_time >= '<t0>' AND event_time < '<t1>'
  AND parent_path LIKE '%/mergetree/_objects%'
-- 成功请求；用 `echo mntr | nc <keeper> 9181`（zk_znode_count）随时间确认增长本身
GROUP BY minute ORDER BY minute
```
**升级案例踩过的坑：** 没有`operation` / `is_subrequest`过滤的总计会把每个Multi计两次（它的外壳，其`parent_path`为空，以及它的子请求）— 曾经247M次Get位于112k次MultiRead之内；`errors` map包含`ZOK`，所以在统计失败前要过滤掉它；只按`count`加权平均`average_latency`；每台服务器只记录自己的会话，所以排名不能说明同一Keeper的其他客户端的情况；并且始终与基线窗口比较 — 没有基线的top-N无法显示变化。如果比较很重要，在回滚或降级之前采集该表。

## 4. 磁盘与存储

```sql
SELECT name, type, free_pct, free_space, total_space FROM file('$B/system.disks_*.jsonl', JSONEachRow)
```
（`free_space`/`total_space`是字符串；`free_pct`是数字；cloud每个replica一行。）交叉检查`host_info.json → disks[]`，其中`mount_point`覆盖`configuration/`中ClickHouse的`path`。按database/engine从`system.parts`（`active = 1`、`sum(bytes_on_disk)`）统计大小，压缩比 = `data_uncompressed_bytes / data_compressed_bytes`。

## 4a. 磁盘是瓶颈吗？（在merge或插入看起来慢时添加）

每小时存储流量和因它而阻塞的线程。每个计数器都是跨线程求和，因此每个都要除以该小时采样覆盖的秒数 — 而不是除以CPU数。这些秒数是`samples × interval`，间隔（`metric_log`的`collect_interval_milliseconds`，默认1 s，可配置，不在任何系统表中）从bundle推导：取任何有两份或更多采样的小时中最紧的间距，因为间隔只会让它变宽。值在文件中是带引号的（UInt64），所以算术前先cast：

```sql
SELECT time,
       toUInt64(metric_samples) AS samples,
       minIf(toUInt64(span_s) / (samples - 1), samples >= 2) OVER () AS interval_s,
       samples * interval_s AS secs,
       greatest(0, round(toUInt64(span_s) / interval_s) + 1 - samples) AS missed_samples,
       round(toUInt64("sum(ProfileEvent_OSReadBytes)")  / 1048576 / secs, 1) AS read_mib_s,
       round(toUInt64("sum(ProfileEvent_OSWriteBytes)") / 1048576 / secs, 1) AS write_mib_s,
       round(toUInt64("sum(ProfileEvent_OSIOWaitMicroseconds)") / 1e6 / secs, 2) AS threads_blocked_on_io,
       round(toUInt64("sum(ProfileEvent_DiskReadElapsedMicroseconds)")  / 1e6) AS read_call_thread_s,
       round(toUInt64("sum(ProfileEvent_DiskWriteElapsedMicroseconds)") / 1e6) AS write_call_thread_s,
       round(toUInt64("sum(ProfileEvent_OSReadBytes)") / nullIf(toUInt64("sum(ProfileEvent_ReadBufferFromFileDescriptorReadBytes)"), 0), 2) AS os_vs_asked,
       toUInt64(max_merge_pool_tasks) AS merge_pool_max
FROM file('$B/system.metric_log_7_days_*.jsonl', JSONEachRow)
ORDER BY time
```

在cloud上读取`system.metric_log_by_host_3_days_*.jsonl`，加上`hostname`，并按主机推导间隔：`OVER (PARTITION BY hostname)`。如果没有两个小时或更多采样的小时，间隔就是未知的，速率会算出NULL — 要说明这一点，而不是假设1 s。onprem的`configuration/`副本可能声明了`<metric_log><collect_interval_milliseconds>`，可以确定它。

饱和是**流量达到某个卷的上限**，而计数器无法说明是哪个卷：它们把ClickHouse在每个本地磁盘上的I/O加在一起。在判定某个卷饱和 — 并建议升级它 — 之前，流量必须能映射到它：`system.disks`和存储策略中只有一块本地数据盘，或该卷自身的遥测（iostat的`%util`和`await`、云提供商的按卷吞吐和队列长度）对照其预置上限，而bundle也不携带后者。有多个本地卷时，它们的合计流量可能达到单个卷的上限，而两者都没有饱和。然后用`threads_blocked_on_io`高于bundle自身安静小时、同时`merge_pool_max`处于`background_pool_size`来印证：**设备**就是瓶颈，更大的池只会让队列更长 — 杠杆是更快的存储或更少、更大的part（HC-4.6、P-01）。两列都无法单独定论，而两种读数也不能排除它：

- `os_vs_asked`远低于1意味着页缓存吸收了读取。miss和写入仍然会到达设备并可能把它填满。
- bundle没有I/O操作计数，所以受IOPS限制的卷可能在其吞吐上限以下就饱和。

`read_call_thread_s` / `write_call_thread_s`是调用内部的线程时间，包含缓存命中和重叠调用累加：是上下文，绝不是字节的分母。`missed_samples > 0`的小时发生过停机或启动，启动后的第一个采样可能携带某计数器的累计值（实测：一个1秒采样中出现3.5e12 µs的`OSIOWaitMicroseconds`）— 基于这个证据把该小时视为不可靠，绝不要因为某个值高就如此。在内核不统计`threads_blocked_on_io`的地方它全程为0；那里的0不是“没有等待”。在非Linux服务器上，`OS*`计数器为0，bundle无法测量设备：`ReadBufferFromFileDescriptor*`是逻辑I/O，包含缓存服务，而elapsed计数器是调用时间 — 缓存驻留的工作负载会把两者都推高，而设备流量几乎为零。在那里索取操作系统级磁盘遥测。在数据位于对象存储的服务上，这些列按设计接近零，`metric_log_coordination_3_days`中的S3计数器是等价物（HC-4.10）。

## 5. 内存与CPU

服务器端跟踪的内存和后台池每小时（dashboard（仪表盘）自v0.7起绘制三个池的平均值；内存、公共池和Keeper计数器只在这里出现）：
```sql
SELECT time, formatReadableSize(avg_memory_tracking_bytes) AS mem, round(avg_merge_pool_tasks,1) AS merge_pool,
       max_merge_pool_tasks, round(avg_fetch_pool_tasks,1) AS fetch_pool, max_fetch_pool_tasks,
       round(avg_schedule_pool_tasks,1) AS schedule_pool, max_schedule_pool_tasks,
       round(avg_common_pool_tasks,1) AS common_pool,
       round(max_replica_avg_fetch_pool_tasks,1) AS worst_replica_fetch, round(max_replica_avg_schedule_pool_tasks,1) AS worst_replica_schedule,
       zk_transactions, zk_hw_exceptions
FROM file('$B/system.metric_log_7_days_*.jsonl', JSONEachRow) ORDER BY time
```
（`*_fetch_pool_tasks`的max、schedule / common池列以及`max_replica_avg_*`列 — 最差replica的每小时平均值，cloud中判断饱和的粒度 — 从v0.7起存在；较旧的bundle只有`max_merge_pool_tasks`和`avg_fetch_pool_tasks`。）把`avg_memory_tracking_bytes`的峰值与`host_info.memory.total_bytes`和`clickhouse_relevant_tunables.cgroup_memory_limit_bytes`比较；每个池对照其大小 — 某个池连续数小时钉在它的大小上就是饱和（merge见HC-2.7，fetch见HC-2.13，schedule见HC-2.14；SharedMergeTree上见P-59）：
```sql
SELECT name, value FROM file('$B/system.server_settings_*.jsonl', JSONEachRow)   -- < 23.3 上为 system.settings_*.jsonl
WHERE name IN ('background_pool_size','background_fetches_pool_size','background_schedule_pool_size','background_common_pool_size')
```
预检会以`background pools (last 24 h, hourly avg vs size)`打印同样的比较；告警`fetch_pool_saturated` / `schedule_pool_saturated`按replica在过去24小时中有3小时达到大小的 ≥ 90 % / ≥ 95 % 时触发。

主机视角：`host_info.json` → `cpu.logical_cpus`、`cpu.load_avg_1_5_15`（load ≫ CPU数 = 采集时饱和）、`memory.available_bytes`（< 2 GiB会触发ClickHouse自身的启动警告）、`swap_total_bytes - swap_free_bytes`（正在使用的swap）、`top_processes_by_rss`（ClickHouse是唯一的大进程吗？）。

每查询内存（按模式的最大分配）。使用规范的去重CTE — 无论查询触及多少张表，它都为每个查询组保留一行：
```sql
WITH dedup AS (
  SELECT * FROM file('$B/system.query_log_details_7_days_*.jsonl', JSONEachRow)
  WHERE type != 'QueryStart'
  LIMIT 1 BY time, query_kind, type, user, interface, normalized_query_hash, exception_code)
SELECT normalized_query_hash, any(user) AS user, sum(toUInt64(count)) AS runs,
       formatReadableSize(max(toUInt64(memory_usage)) / argMax(toUInt64(`count`),toUInt64(memory_usage))) AS mem_per_query_est,
       leftUTF8(any(query), 150) AS sample
FROM dedup WHERE type = 'QueryFinish'
GROUP BY 1 ORDER BY max(toUInt64(memory_usage)) DESC LIMIT 10
```
此文件中的`memory_usage`是**小时桶上的总和**，所以除以`count`得到每查询估计。在 §6的每个聚合中复用同一个`dedup` CTE；避免别名`sample`（它是关键字）— 使用`q`/`msg`。

## 6. 查询负载

最慢的模式（每次执行平均，过滤到一个`tables`值）：
```sql
SELECT normalized_query_hash, any(query_kind) AS kind, any(user) AS user,
       sum(toUInt64(count)) AS runs,
       round(sum(toUInt64(query_duration_ms)) / sum(toUInt64(count))) AS avg_ms,
       formatReadableSize(sum(toUInt64(read_bytes)) / sum(toUInt64(count))) AS avg_read,
       leftUTF8(any(query),150) AS sample
FROM file('$B/system.query_log_details_7_days_*.jsonl', JSONEachRow)
WHERE type = 'QueryFinish' AND tables != '' AND query_kind = 'Select'
GROUP BY 1 ORDER BY avg_ms DESC LIMIT 10
```
按错误码分类的失败：`WHERE exception_code != 0` → `exception_code, sum(count), any(exception), max(distinct_exceptions)`；通过`error-codes.md`把错误码连接到名称。每小时按类型的查询：`GROUP BY time, query_kind`。按用户：`GROUP BY user`。区分`type`：`ExceptionBeforeStart`（语法/认证/限制）与`ExceptionWhileProcessing`（运行时：内存、超时）。

## 7. Mutation、TTL、字典、detached part

```sql
SELECT database, table, mutation_id, leftUTF8(command,120) AS command, create_time, parts_to_do
FROM file('$B/system.mutations_*.jsonl', JSONEachRow) ORDER BY create_time LIMIT 20
```
年龄 = 采集时间 − `create_time`（采集时间是文件夹时间戳）。字典：`status != 'LOADED'`、`last_exception != ''`、`bytes_allocated`最大。Detached：按`database, table, reason`分组的`count()`和`sum(bytes_on_disk)`。

## 8. 错误与日志

累计错误计数器（自重启以来 — 与dashboard的`uptime`或`os.uptime_seconds`比较）：
```sql
SELECT code, name, toUInt64(value) AS total, last_error_time, leftUTF8(last_error_message,200) AS msg
FROM file('$B/system.errors_*.jsonl', JSONEachRow) ORDER BY total DESC LIMIT 15
```
text_log切片：按`level, logger_name`计数，然后读取最新的20条`Error`/`Fatal`行。检查`min(event_time)` — 2000行可能只跨越几分钟。

日志文件（grep，绝不整份读取）：
```bash
grep -c '' "$B"/logs/clickhouse-server.err.log
grep -oE '<(Error|Fatal|Warning)> [A-Za-z0-9_.:()]+' "$B"/logs/clickhouse-server.err.log | sort | uniq -c | sort -rn | head -20
grep -oE 'Code: [0-9]+' "$B"/logs/clickhouse-server.err.log | sort | uniq -c | sort -rn | head -20
grep -nE 'Starting ClickHouse|Ready for connections|Received termination signal|Shutting down|<Fatal>' "$B"/logs/clickhouse-server.log | head -20
grep -nE 'Available memory|transparent huge|overcommit|TaskStats|Linux' "$B"/logs/clickhouse-server.log | head   # 启动警告
```
同一条相同行重复数百次就是循环（重试merge、卡住的fetch）— 计数即可，不要阅读。

## 9. 来自`system.tables`的schema事实

```sql
SELECT database, name, engine, partition_key, sorting_key, storage_policy
FROM file('$B/system.tables_*.jsonl', JSONEachRow)
WHERE database NOT IN ('system','INFORMATION_SCHEMA','information_schema') AND engine LIKE '%MergeTree%'
ORDER BY database, name LIMIT 50
```
与 §2连接以解释part数量：高基数`partition_key`（按小时、按租户）+ 许多分区 = 预期的扇出；空的`sorting_key`（`tuple()`）= 没有主索引。`engine_full`携带按表的`SETTINGS`覆盖（`parts_to_throw_insert`、`ttl_only_drop_parts`、`min_age_to_force_merge_seconds`…）。MV链：`engine = 'MaterializedView'` + `as_select`；从`dependencies_table`统计每个源表的MV数量。

## 9a. 已更改的设置

```sql
SELECT 'query' AS scope, name, value, `default` FROM file('$B/system.settings_*.jsonl', JSONEachRow) WHERE changed = 1
UNION ALL
SELECT 'server', name, value, `default` FROM file('$B/system.server_settings_*.jsonl', JSONEachRow) WHERE changed = 1
ORDER BY scope, name
```
（`default`在 < 23.4的根上不存在 — 在那里去掉该列。）把池/内存行与`metric_log`和`host_info`交叉检查；`REMOVED`值是gov对可识别服务器设置的脱敏，不是错误。

## 10. 查询分析文件（`A=$B/query_analysis`）

先识别形态 — 其他一切都依赖它：
```sql
SELECT query_id, query_kind, is_initial_query, user, initial_user, hostname, tables, databases, read_rows,
       formatReadableSize(toUInt64(read_bytes)) rd, query_duration_ms, memory_usage_human, peak_threads_usage, exception_code
FROM file('$A/query_details_*.jsonl', JSONEachRow)
```
像`['_table_function.s3Cluster']`这样的`tables` → 没有MergeTree part（跳过`text_log_parts`/`tables_for_query`）；`is_initial_query = 0` → worker子查询，找到父查询：`grep -o 'initial_query_id: [^)]*' $A/text_log_full_*.jsonl | head -1`。

重点执行的时间预算（微秒计数器占墙钟时间的份额）。墙钟时间是`query_duration_ms`；`RealTimeMicroseconds`是**跨线程求和**的，会高估它：
```sql
WITH (SELECT toUInt64(query_duration_ms) * 1000 FROM file('$A/query_details_*.jsonl', JSONEachRow)) AS wall
SELECT metric, round(toUInt64(value)/1e6, 1) AS seconds, round(100 * toUInt64(value) / wall, 1) AS pct_of_wall
FROM file('$A/profile_events_*.jsonl', JSONEachRow)
WHERE metric IN ('RealTimeMicroseconds','OSCPUVirtualTimeMicroseconds','OSCPUWaitMicroseconds','UserTimeMicroseconds','SystemTimeMicroseconds',
  'NetworkSendElapsedMicroseconds','NetworkReceiveElapsedMicroseconds','ParquetFetchWaitTimeMicroseconds','ReadBufferFromS3Microseconds',
  'S3ReadMicroseconds','DiskReadElapsedMicroseconds','DiskWriteElapsedMicroseconds','ZooKeeperWaitMicroseconds','ThreadPoolReaderPageCacheMissElapsedMicroseconds')
ORDER BY toUInt64(value) DESC
```
这样读：CPU使用（`OSCPUVirtualTime`）与CPU等待（`OSCPUWait` ≈ CPU使用 = 被限流）；网络发送份额（发往发起方/客户端的行）；存储等待；其余是排队/锁时间。与之配对的大小：`SelectedRows/Bytes`、`SelectedParts/Marks`、`NetworkSendBytes`、`MemoryAllocatedWithoutCheckBytes`。

运行还是shard（分片）？
```sql
SELECT ts, leftUTF8(hostname, 30) host, read_rows, formatReadableSize(toUInt64(read_bytes)) rd, query_duration_ms,
       round(toUInt64(read_rows) / greatest(toUInt64(query_duration_ms), 1) * 1000) rows_per_s, memory_usage_human, exception_code
FROM file('$A/executions_timeline_*.jsonl', JSONEachRow) ORDER BY ts
```
同一分钟、不同主机，`rows_per_s`相近而`read_rows`不同 → shards（数据在replica/文件间倾斜）。不同时间、同一主机 → 多次运行；用`profile_events_compare`比较它们（`ORDER BY abs(toInt64(delta)) DESC LIMIT 20`）。

一次执行内部时间花在了哪里（`text_log_full`）：
```sql
SELECT prev, ts, dateDiff('second', prev, ts) gap_s, leftUTF8(message, 120) m
FROM (SELECT ts, lagInFrame(ts) OVER (ORDER BY ts) prev, message FROM file('$A/text_log_full_*.jsonl', JSONEachRow))
WHERE gap_s > 10 ORDER BY gap_s DESC LIMIT 5
```
另外对对象存储源使用`grep -o 'Reading object .*size: [0-9]* bytes' $A/text_log_full_*.jsonl`（文件大小揭示倾斜），对MergeTree源使用`text_log_parts`（"Selected N/M parts by partition key, K/L marks" — N = M意味着没有part剪枝）。

失败：`failed_over_time`（按`error_type`每分钟）、`failed_queries`（按表 × 错误码 × 用户，带`sample_exception`）。为空 = 窗口内没有失败。

## 11. gov模式连接

Hash按salt保持稳定：对同一个真实表，`system.parts.table = system.replicas.table = system.mutations.table`。按hash聚合和排序；用前8个十六进制字符标记findings。如果用户本地有`*_gov_name_mapping.csv`，**在他们的机器上**用`clickhouse local`或Python连接`table_hash` → `table`；不要把CSV粘贴到任何地方。