-- tier: essential
-- 窗口：3 天。part_log 记录每个 part 事件（NewPart、MergeParts、DownloadPart、RemovePart …），繁忙副本上每周可达数十 GiB；
-- 三天能在事件附近保留基线，扫描量却只是一个零头。
-- 按主机逐小时，像 23.11+ 变体一样（hostName()：part_log 在 23.11
-- 之前没有 hostname 列），所以各版本上的告警检查都读到相同
-- 形状。
SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    event_type,
    merge_reason,
    partition_id,
    hex(SHA256(concat(hostName(), '%salt%'))) AS hostname,
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%'))) AS table,
    error,
    sum(peak_memory_usage) AS peak_memory_usage,
    sum(duration_ms) AS duration_ms,
    sum(size_in_bytes) AS size_in_bytes,
    count() as count
FROM system.part_log
-- 有意完全不采集 part_name，任何形式都不采集。
--
-- 作为 GROUP BY 键是灾难性的：它每个 part 都唯一，所以把它作为键会
-- 把这次“聚合”变成 system.part_log 的逐行拷贝（玩具数据集上 868 个事件
-- 产生 868 行；生产环境 7 天就是数百万行）。这正是本文件要避免的回归，
-- 所以不要把它加回键里。
--
-- 用 any(part_name) 采样则只是无用：在分组覆盖的众多 part 中取一个
-- 任意值，且无法说明为什么选中它。
-- 它不是可用于排查的句柄。
-- 有用的粒度是 bucket x table x event_type x merge_reason x partition
-- x error，后续跟进应使用 partition_id 这个标识符。
WHERE (event_time > {from:3d} AND event_time <= {to:now})
-- 显式列出键，而不用 GROUP BY ALL：该语法需要 22.12+，
-- 而这个根目录文件必须能在所有受支持的服务器（22.8+）上运行。
GROUP BY time, event_type, merge_reason, partition_id, hostname,
         database, table, error
