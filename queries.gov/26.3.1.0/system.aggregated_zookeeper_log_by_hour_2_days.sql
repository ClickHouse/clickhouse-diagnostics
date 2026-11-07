-- 最近两天每小时、按 ClickHouse 组件和操作统计的 Keeper 请求：
-- 服务器中是谁在给 Keeper 施加负载、用什么操作、有多慢。
-- system.aggregated_zookeeper_log（自 25.9 默认开启）每秒按会话、
-- 父路径、操作和组件对所有 Keeper 请求做一次分组；
-- 这里按小时 × 组件 × 操作读取它。
--   ops        独立请求（一个 Multi 只计一次）
--   in_multi   Multi / MultiRead 内部携带的子请求 —— 批量读取就藏在
--              这里（数百万次 Get 可以塞进少数几个 Multi）
--   avg_latency_us  按 count 加权，仅独立请求：子请求报 0，
--              因为由外层 Multi 承担
--   errors     每个非 ZOK 错误码及其数量
-- 仅版本目录（26.3+）：需要 `component`（26.2）和 `is_subrequest`（26.3）；
-- 26.3 之前子请求与独立请求共用桶，延迟会误导，
-- 所以旧服务器跳过本文件。
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
