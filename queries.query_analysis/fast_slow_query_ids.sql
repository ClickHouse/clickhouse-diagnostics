-- 目标 hash 的两个代表性 query_id：最慢的
-- 执行（按 duration 取 argMax）和最快的（argMin）。它们
-- 成为 profile_events_compare.sql 的输入。
-- 当设置了 --normalized-query-hash 时运行（由 --query-id 自动推导）。
-- toString(normalized_query_hash) 被别名为 *_str，这样该别名不会
-- 在 WHERE 子句中遮蔽源 UInt64 列（否则 CH 25.12 的
-- 分析器会将 `normalized_query_hash = 12345` 解析为针对
-- String 别名 → NO_COMMON_TYPE）。
SELECT
    toString(any(normalized_query_hash))                    AS normalized_query_hash_str,
    argMax(query_id, query_duration_ms)                     AS slow_query_id,
    argMin(query_id, query_duration_ms)                     AS fast_query_id,
    toString(argMax(event_time, query_duration_ms))         AS slow_event_time,
    toString(argMin(event_time, query_duration_ms))         AS fast_event_time,
    max(query_duration_ms)                                  AS slow_duration_ms,
    min(query_duration_ms)                                  AS fast_duration_ms,
    count()                                                 AS executions
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type = 'QueryFinish'
  AND NOT has(databases, 'system')
