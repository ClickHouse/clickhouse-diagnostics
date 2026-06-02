-- Single consolidated time-series for the focus hash. Each row is one
-- hour bucket within the window. The dashboard front-end derives
-- multiple charts from the same array (executions count, p95
-- duration, sum memory, sum userCPU, sum duration, read rows/bytes),
-- so we run ONE aggregation instead of one query per chart.
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
SELECT
    toString(toStartOfHour(event_time))                     AS time_bucket,
    -- counts
    count()                                                 AS executions,
    countIf(type = 'QueryFinish')                           AS succeeded,
    countIf(type != 'QueryFinish' AND type != 'QueryStart') AS failed,
    -- duration distribution
    round(avg(query_duration_ms), 0)                        AS avg_duration_ms,
    round(quantile(0.5)(query_duration_ms), 0)              AS p50_duration_ms,
    round(quantile(0.95)(query_duration_ms), 0)             AS p95_duration_ms,
    max(query_duration_ms)                                  AS max_duration_ms,
    sum(query_duration_ms)                                  AS sum_duration_ms,
    -- memory
    sum(memory_usage)                                       AS sum_memory_bytes,
    round(sum(memory_usage) / 1048576, 2)                   AS sum_memory_mb,
    -- CPU — UserTimeMicroseconds is the user-CPU spent by the query
    -- across all of its threads. Convert to seconds for a readable
    -- dashboard axis.
    round(sum(ProfileEvents['UserTimeMicroseconds']) / 1e6, 4) AS sum_user_cpu_sec,
    -- I/O
    sum(read_rows)                                          AS sum_read_rows,
    sum(read_bytes)                                         AS sum_read_bytes,
    formatReadableSize(sum(read_bytes))                     AS sum_read_bytes_human,
    sum(written_rows)                                       AS sum_written_rows,
    sum(written_bytes)                                      AS sum_written_bytes
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
GROUP BY time_bucket
ORDER BY time_bucket
FORMAT Native
