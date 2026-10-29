-- Keeper requests per hour, ClickHouse component and operation over the last
-- two days, across every replica: WHO in the server is loading Keeper, with
-- what, and how slowly. system.aggregated_zookeeper_log (on by default since
-- 25.9) groups every Keeper request by session, parent path, operation and
-- component once a second; this reads it at hour × component × operation.
--   ops        standalone requests (a Multi counts once)
--   in_multi   sub-requests carried inside Multi / MultiRead — where batched
--              reads hide (millions of Gets can sit inside a few Multis)
--   avg_latency_us  weighted by count, standalone requests only: a
--              sub-request reports 0 because the enclosing Multi carries it
--   errors     every non-ZOK error code with its count
--   replicas / busiest_replica / busiest_replica_ops   rows are summed over
--              the replicas to keep the file small on a large service; the
--              replica that sent most of the row's requests is kept, so a
--              single misbehaving node still shows (per-replica minutes are
--              in aggregated_zookeeper_log_by_minute_1_day)
-- Version-only (26.3+): needs `component` (26.2) and `is_subrequest` (26.3);
-- before 26.3 sub-requests share buckets with standalone requests and the
-- latency is misleading, so older servers skip this file.
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
