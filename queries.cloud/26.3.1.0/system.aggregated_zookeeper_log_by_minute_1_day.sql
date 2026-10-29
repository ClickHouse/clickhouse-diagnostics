-- Keeper requests per minute and replica over the last day: WHEN Keeper got
-- slow, busy or started failing, and on WHICH replica — the minute of a latency jump, a restart
-- burst, or traffic collapsing. Built from system.aggregated_zookeeper_log
-- (on by default since 25.9).
--   ops / in_multi      standalone requests / sub-requests inside Multi
--   avg_latency_us      weighted by count, standalone requests only
--   worst_group_latency_us  slowest (session, path, operation) group that minute
--   failed              non-ZOK responses; loss_failures the subset that means
--                       the session itself was in trouble (connection loss,
--                       session expired, operation timeout)
-- Up to 1 440 rows per replica (a 29-replica service is about 42 000).
-- Version-only (26.3+, needs `is_subrequest`): older servers skip this file.
SELECT
    toStartOfMinute(event_time)                                     AS time,
    hostname,
    sumIf(count, is_subrequest = 0)                                 AS ops,
    sumIf(count, is_subrequest = 1)                                 AS in_multi,
    round(sumIf(average_latency * count, is_subrequest = 0) / nullIf(ops, 0)) AS avg_latency_us,
    maxIf(average_latency, is_subrequest = 0)                       AS worst_group_latency_us,
    sum(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))) AS failed,
    sum(arraySum(mapValues(mapFilter((code, n) -> code IN ('ZCONNECTIONLOSS', 'ZSESSIONEXPIRED', 'ZOPERATIONTIMEOUT'), errors)))) AS loss_failures,
    uniq(session_id)                                                AS sessions
FROM clusterAllReplicas(default, system.aggregated_zookeeper_log)
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY time, hostname
ORDER BY time, hostname
LIMIT 200000
