-- Time-bucketed summary of the focus hash. One row per hour
-- (within the requested window): execution count, success vs failure
-- split, latency quantiles, and read/memory averages. Feeds the
-- dashboard's "executions over time" chart.
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
SELECT
    toString(toStartOfHour(event_time))                     AS hour,
    count()                                                 AS executions,
    countIf(type = 'QueryFinish')                           AS succeeded,
    countIf(type != 'QueryFinish' AND type != 'QueryStart') AS failed,
    round(avg(query_duration_ms), 0)                        AS avg_duration_ms,
    round(quantile(0.5)(query_duration_ms), 0)              AS p50_duration_ms,
    round(quantile(0.95)(query_duration_ms), 0)             AS p95_duration_ms,
    max(query_duration_ms)                                  AS max_duration_ms,
    round(avg(memory_usage) / 1048576, 2)                   AS avg_memory_mb,
    round(avg(read_rows), 0)                                AS avg_read_rows,
    formatReadableSize(round(avg(read_bytes), 0))           AS avg_read_bytes_human,
    round(avg(read_bytes), 0)                               AS avg_read_bytes
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
GROUP BY hour
ORDER BY hour
FORMAT Native
