-- Failed-execution count per minute, split by exception code. Drives
-- the stacked "FAILED Queries count" chart in the dashboard so a spike
-- of (say) MEMORY_LIMIT_EXCEEDED is distinguishable from a spike of
-- TIMEOUT_EXCEEDED.
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
SELECT
    toString(toStartOfMinute(event_time))                   AS time_bucket,
    errorCodeToName(exception_code) || ' (' || toString(exception_code) || ')' AS error_type,
    count()                                                 AS errors
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type NOT IN ('QueryFinish', 'QueryStart')
  AND NOT has(databases, 'system')
GROUP BY time_bucket, error_type
ORDER BY time_bucket, errors DESC
