-- 过去两天的 Keeper 请求，按小时、ClickHouse 组件和操作分组：
-- 服务器里的“谁”在给 Keeper 施加负载、用什么操作、有多慢。
-- system.aggregated_zookeeper_log（自 25.9 起默认开启）每秒把每个
-- Keeper 请求按 session、父路径、操作和组件聚合一次；
-- 这里按 小时 × 组件 × 操作 来读取它。
--   ops        独立请求（一个 Multi 只计一次）
--   in_multi   承载在 Multi / MultiRead 内部的子请求——批量读取
--              就藏在这里（数百万次 Get 可以塞进少数几个 Multi）
--   avg_latency_us  按 count 加权，只统计独立请求：
--              子请求报告 0，因为耗时由外层的 Multi 承担
--   errors     每个非 ZOK 错误码及其计数
-- 仅版本目录（26.3+）：需要 `component`（26.2）和 `is_subrequest`（26.3）；
-- 26.3 之前子请求与独立请求共享 bucket，延迟会失真，
-- 因此更旧的服务器会跳过这个文件。
SELECT
    toStartOfHour(event_time)                                       AS time,
    component,
    operation,
    sumIf(count, is_subrequest = 0)                                 AS ops,
    sumIf(count, is_subrequest = 1)                                 AS in_multi,
    round(sumIf(average_latency * count, is_subrequest = 0) / nullIf(ops, 0)) AS avg_latency_us,
    maxIf(average_latency, is_subrequest = 0)                       AS worst_group_latency_us,
    sumMap(mapFilter((code, n) -> code != 'ZOK', errors))           AS errors,
    uniq(session_id)                                                AS sessions
FROM system.aggregated_zookeeper_log
WHERE event_date >= toDate({from:2d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:2d} AND event_time <= {to:now}
GROUP BY time, component, operation
ORDER BY time, ops + in_multi DESC
LIMIT 50000
