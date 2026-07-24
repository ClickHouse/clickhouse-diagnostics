-- Lives in 22.10.1.0/ with no root counterpart: the
-- asynchronous_insert_log table itself was added in 22.10, so older
-- servers simply skip this file instead of erroring on a missing table.
--
-- We report bytes rather than rows: the `rows` column was added to
-- asynchronous_insert_log after 22.12 (22.10–22.12 have `bytes` but not
-- `rows`), and `bytes` covers the whole 22.10+ range with a single file.
SELECT
    toStartOfHour(event_time)           AS time,
    database,
    table,
    status,
    count()                             AS flushes,
    sum(bytes)                          AS total_bytes,
    -- Latency in ms via float subtraction rather than
    -- dateDiff('millisecond', …): the sub-second unit isn't supported on
    -- 22.10–22.12 (BAD_ARGUMENTS). This form works across 22.10+ (full
    -- precision once flush_time becomes DateTime64 on newer servers).
    round(avg((toFloat64(flush_time) - toFloat64(event_time)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time) - toFloat64(event_time)) * 1000), 0)  AS p90_flush_ms
FROM system.asynchronous_insert_log
WHERE event_time > now() - INTERVAL 7 DAY
-- Explicit key list instead of GROUP BY ALL (needs 22.12+); this file
-- must run on 22.10 and 22.11 too.
GROUP BY time, database, table, status
ORDER BY time, database, table
FORMAT Native
