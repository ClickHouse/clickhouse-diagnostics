-- Keeper requests per hour, ClickHouse component and operation over the last
-- two days: WHO in the server is loading Keeper, with what, and how slowly.
-- system.aggregated_zookeeper_log (on by default since 25.9) groups every
-- Keeper request by session, parent path, operation and component once a
-- second; this reads it at hour × component × operation.
--   ops        standalone requests (a Multi counts once)
--   in_multi   sub-requests carried inside Multi / MultiRead — where batched
--              reads hide (millions of Gets can sit inside a few Multis)
--   avg_latency_us  weighted by count, standalone requests only: a
--              sub-request reports 0 because the enclosing Multi carries it
--   errors     every non-ZOK error code with its count
-- Version-only (26.3+): needs `component` (26.2) and `is_subrequest` (26.3);
-- before 26.3 sub-requests share buckets with standalone requests and the
-- latency is misleading, so older servers skip this file.
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
