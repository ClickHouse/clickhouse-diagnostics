SELECT
    toStartOfHour(event_time)           AS time,
    database,
    table,
    status,
    count()                             AS flushes,
    sum(rows)                           AS total_rows,
    sum(bytes)                          AS total_bytes,
    round(avg(dateDiff('millisecond', event_time, flush_time)), 0)            AS avg_flush_ms,
    round(quantile(0.9)(dateDiff('millisecond', event_time, flush_time)), 0)  AS p90_flush_ms
-- Lives in 22.10.1.0/ with no root counterpart: the
-- asynchronous_insert_log table itself was added in 22.10, so older
-- servers simply skip this file instead of erroring on a missing table.
FROM system.asynchronous_insert_log
WHERE event_time > now() - INTERVAL 7 DAY
-- Explicit key list instead of GROUP BY ALL (needs 22.12+); this file
-- must run on 22.10 and 22.11 too.
GROUP BY time, database, table, status
ORDER BY time, database, table
FORMAT Native
