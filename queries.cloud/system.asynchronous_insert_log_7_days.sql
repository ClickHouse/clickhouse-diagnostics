SELECT
    toStartOfHour(event_time)           AS time,
    database,
    table,
    status,
    count()                             AS flushes,
    sum(rows)                           AS total_rows,
    sum(bytes)                          AS total_bytes,
    round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)  AS p90_flush_ms
FROM clusterAllReplicas(default, merge(system, '^asynchronous_insert_log'))
WHERE event_time > now() - INTERVAL 7 DAY
GROUP BY ALL
ORDER BY time, database, table
FORMAT Native
