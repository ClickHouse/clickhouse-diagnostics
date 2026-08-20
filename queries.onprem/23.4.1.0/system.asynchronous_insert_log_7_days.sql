-- 23.4+ variant: adds total_rows. The `rows` column was added to
-- system.asynchronous_insert_log in 23.4 (absent on 22.10–23.3, which use
-- the bytes-only variant in 22.10.1.0/).
SELECT
    toStartOfHour(event_time)           AS time,
    database,
    table,
    status,
    count()                             AS flushes,
    sum(rows)                           AS total_rows,
    sum(bytes)                          AS total_bytes,
    -- Latency in ms from the DateTime64(6) *_microseconds columns via
    -- float subtraction — identical to the 22.10.1.0/ rung so both files
    -- compute latency the same way (that rung needs it to avoid
    -- dateDiff('millisecond',…), unsupported on 22.10–22.12).
    round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)  AS p90_flush_ms
FROM system.asynchronous_insert_log
WHERE event_time > now() - INTERVAL 7 DAY
GROUP BY time, database, table, status
ORDER BY time, database, table
