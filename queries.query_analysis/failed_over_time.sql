-- 按分钟统计的失败执行次数，按异常码拆分。驱动
-- dashboard 中的堆叠 "FAILED Queries count" 图表，使（例如）
-- MEMORY_LIMIT_EXCEEDED 的尖峰能够与
-- TIMEOUT_EXCEEDED 的尖峰区分开。
-- 当设置了 --normalized-query-hash 时运行（由 --query-id 自动推导）。
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
