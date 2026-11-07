-- 过去一天的 Keeper 请求，按分钟统计：Keeper 何时变慢、变忙，
-- 或开始对本服务器失败——延迟跳升、重启爆发或流量骤降的那一分钟。
-- 数据来自 system.aggregated_zookeeper_log
-- （自 25.9 起默认开启）。
--   ops / in_multi      独立请求 / Multi 内部的子请求
--   avg_latency_us      按 count 加权，只统计独立请求
--   worst_group_latency_us  该分钟最慢的 (session, path, operation) 组
--   failed              非 ZOK 响应；loss_failures 是其中表示
--                       会话本身出问题的子集（连接丢失、
--                       会话过期、操作超时）
-- 仅版本目录（26.3+，需要 `is_subrequest`）：更旧的服务器会跳过这个文件。
SELECT
    toStartOfMinute(event_time)                                     AS time,
    sumIf(count, is_subrequest = 0)                                 AS ops,
    sumIf(count, is_subrequest = 1)                                 AS in_multi,
    round(sumIf(average_latency * count, is_subrequest = 0) / nullIf(ops, 0)) AS avg_latency_us,
    maxIf(average_latency, is_subrequest = 0)                       AS worst_group_latency_us,
    sum(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))) AS failed,
    sum(arraySum(mapValues(mapFilter((code, n) -> code IN ('ZCONNECTIONLOSS', 'ZSESSIONEXPIRED', 'ZOPERATIONTIMEOUT'), errors)))) AS loss_failures,
    uniq(session_id)                                                AS sessions
FROM system.aggregated_zookeeper_log
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY time
ORDER BY time
LIMIT 20000
