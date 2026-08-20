-- Lives in 22.10.1.0/ with no root counterpart: the
-- asynchronous_insert_log table itself was added in 22.10, so older
-- servers simply skip this file instead of erroring on a missing table.
--
-- This 22.10–23.3 variant reports bytes only: the `rows` column was
-- added in 23.4 — see 23.4.1.0/system.asynchronous_insert_log_7_days.sql
-- for the variant that also reports total_rows.
SELECT
    toStartOfHour(event_time)                               AS time,
    hex(SHA256(concat(database, '%salt%')))                 AS database,
    hex(SHA256(concat(table, '%salt%')))                    AS table,
    status,
    count()                                                 AS flushes,
    sum(bytes)                                              AS total_bytes,
    -- Latency in ms from the DateTime64(6) *_microseconds columns via float
    -- subtraction (avoids dateDiff('millisecond',…), unsupported 22.10–22.12).
    round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)  AS p90_flush_ms
FROM system.asynchronous_insert_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
-- Explicit key list instead of GROUP BY ALL (needs 22.12+).
GROUP BY time, database, table, status
ORDER BY time, database, table
