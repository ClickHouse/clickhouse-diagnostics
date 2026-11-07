-- 目标 hash 失败执行的按表 × 按错误细分。
-- 驱动 "Failed queries" 明细表（参考 Grafana dashboard 中的
-- panel 116）。
-- 当设置了 --normalized-query-hash 时运行（由 --query-id 自动推导）。
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
