-- Keeper traffic per hour by request type, operation and error, with the number
-- of distinct sessions (session churn = expirations) and latency. Answers "when
-- did Keeper stop answering, which operations failed, how many sessions did this
-- server burn". Table exists only when <zookeeper_log> is configured; it is
-- large (millions of rows per hour on busy clusters), so the window is one day
-- and only five small columns are read. Root variant: duration_ms (< 24.3).
SELECT
    toStartOfHour(event_time)                AS time,
    type,
    op_num,
    error,
    count()                                  AS requests,
    uniq(session_id)                         AS sessions,
    quantile(0.99)(duration_ms)              AS p99_duration_ms,
    max(duration_ms)                         AS max_duration_ms
FROM system.zookeeper_log
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY time, type, op_num, error
ORDER BY time, requests DESC
LIMIT 20000
