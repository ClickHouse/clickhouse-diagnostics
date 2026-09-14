-- Keeper traffic per hour and replica. Cloud never runs < 24.3, so
-- duration_microseconds is used directly. Table exists only when configured.
SELECT
    toStartOfHour(event_time)                          AS time,
    hostName()                                         AS hostname,
    type,
    op_num,
    error,
    count()                                            AS requests,
    uniq(session_id)                                   AS sessions,
    quantile(0.99)(duration_microseconds) / 1000       AS p99_duration_ms,
    max(duration_microseconds) / 1000                  AS max_duration_ms
FROM clusterAllReplicas(default, system.zookeeper_log)
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY time, hostname, type, op_num, error
ORDER BY time, requests DESC
LIMIT 20000
