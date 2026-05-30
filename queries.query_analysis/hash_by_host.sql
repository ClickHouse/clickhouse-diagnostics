-- Per-host distribution of the focus hash. Surfaces "one node is
-- slower than the rest" patterns in cloud clusters.
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
SELECT
    hostname,
    count()                                                 AS executions,
    max(query_duration_ms)                                  AS max_duration_ms,
    min(query_duration_ms)                                  AS min_duration_ms,
    round(avg(query_duration_ms), 0)                        AS avg_duration_ms,
    round(quantile(0.95)(query_duration_ms), 0)             AS p95_duration_ms,
    formatReadableSize(round(avg(memory_usage), 0))         AS avg_memory,
    formatReadableSize(max(memory_usage))                   AS max_memory,
    countIf(exception_code != 0)                            AS errors
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
GROUP BY hostname
ORDER BY max_duration_ms DESC
FORMAT Native
