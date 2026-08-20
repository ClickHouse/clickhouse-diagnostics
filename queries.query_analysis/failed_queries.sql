-- Per-table × per-error breakdown of failed executions of the focus
-- hash. Drives the "Failed queries" detail table (panel 116 in the
-- reference Grafana dashboard).
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
SELECT
    arrayStringConcat(tables, ', ')                         AS tables_touched,
    errorCodeToName(exception_code) || ' (' || toString(exception_code) || ')' AS error_type,
    user,
    count()                                                 AS errors,
    toString(min(event_time))                               AS first_seen,
    toString(max(event_time))                               AS last_seen,
    max(query_duration_ms)                                  AS max_duration_ms,
    any(exception)                                          AS sample_exception,
    any(query)                                              AS sample_query
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type NOT IN ('QueryFinish', 'QueryStart')
  AND NOT has(databases, 'system')
GROUP BY tables_touched, error_type, user
ORDER BY errors DESC
LIMIT 200
