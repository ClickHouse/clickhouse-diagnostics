-- 过去两天每小时、按 ClickHouse 组件和操作统计的 Keeper 请求，
-- 覆盖每个副本：服务器中“谁”在给 Keeper 施加负载、用什么操作，
-- 以及有多慢。system.aggregated_zookeeper_log（自
-- 25.9 起默认开启）每秒按 session、parent path、operation 和
-- component 对每个 Keeper 请求分组；这里按 hour × component × operation 读取。
--   ops        独立请求（一个 Multi 只计一次）
--   in_multi   承载在 Multi / MultiRead 内部的子请求——批量
--              读取隐藏在其中（数百万次 Get 可以藏在少数几个 Multi 里）
--   avg_latency_us  按 count 加权，仅统计独立请求：
--              子请求报告 0，因为外层 Multi 承载了它
--   errors     每个非 ZOK 错误码及其计数
--   replicas / busiest_replica / busiest_replica_ops   行会在副本间求和，
--              以在大型服务上保持文件较小；
--              保留发送该行最多请求的副本，因此
--              单个行为异常的节点仍会暴露（按副本的分钟数据见
--              aggregated_zookeeper_log_by_minute_1_day）
-- 仅限特定版本（26.3+）：需要 `component`（26.2）和 `is_subrequest`（26.3）；
-- 在 26.3 之前，子请求与独立请求共享 bucket，
-- 延迟会产生误导，因此更旧的服务器会跳过本文件。
SELECT
    time,
    component,
    operation,
    sum(h_ops)                                      AS ops,
    sum(h_in_multi)                                 AS in_multi,
    round(sum(h_lat) / nullIf(ops, 0))              AS avg_latency_us,
    max(h_worst)                                    AS worst_group_latency_us,
    sumMap(h_errors)                                AS errors,
    count()                                         AS replicas,
    argMax(hostname, h_ops + h_in_multi)            AS busiest_replica,
    max(h_ops + h_in_multi)                         AS busiest_replica_ops
FROM
(
    SELECT
        toStartOfHour(event_time)                                       AS time,
        hostname,
        component,
        operation,
        sumIf(count, is_subrequest = 0)                                 AS h_ops,
        sumIf(count, is_subrequest = 1)                                 AS h_in_multi,
        sumIf(average_latency * count, is_subrequest = 0)               AS h_lat,
        maxIf(average_latency, is_subrequest = 0)                       AS h_worst,
        sumMap(mapFilter((code, n) -> code != 'ZOK', errors))           AS h_errors
    FROM clusterAllReplicas(default, system.aggregated_zookeeper_log)
    WHERE event_date >= toDate({from:2d}, timezone()) AND event_date <= toDate({to:now}, timezone())
      AND event_time > {from:2d} AND event_time <= {to:now}
    GROUP BY time, hostname, component, operation
)
GROUP BY time, component, operation
ORDER BY time, ops + in_multi DESC
LIMIT 50000
