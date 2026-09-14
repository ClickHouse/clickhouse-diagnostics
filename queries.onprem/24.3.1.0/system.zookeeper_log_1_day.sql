-- 24.3+ variant: zookeeper_log.duration_ms became duration_microseconds.
-- Output names are kept in milliseconds so the file reads the same on every version.
SELECT
    toStartOfHour(event_time)                          AS time,
    type,
    op_num,
    error,
    count()                                            AS requests,
    uniq(session_id)                                   AS sessions,
    quantile(0.99)(duration_microseconds) / 1000       AS p99_duration_ms,
    max(duration_microseconds) / 1000                  AS max_duration_ms
FROM system.zookeeper_log
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY time, type, op_num, error
ORDER BY time, requests DESC
LIMIT 20000
