-- 目标 query_id 的完整 query_log 行。
-- 当设置了 --query-id 时运行。
--
-- 面向最旧受支持服务器（22.8+）的根变体。后来加入的列
-- 由版本覆盖提供：
--   23.8.1.0/  → query_cache_usage      （23.8 新增）
--   23.9.1.0/  → peak_threads_usage     （23.9 新增）
--   23.11.1.0/ → hostname column        （23.11 新增；根文件使用 hostName()）
SELECT
    event_time_microseconds                                AS ts,
    query_id,
    query_kind,
    type,
    user,
    initial_user,
    is_initial_query,
    hostName()                                             AS hostname,
    address,
    initial_address,
    read_rows,
    read_bytes,
    written_rows,
    written_bytes,
    result_rows,
    query_duration_ms,
    memory_usage                                           AS memory_usage_bytes,
    formatReadableSize(memory_usage)                       AS memory_usage_human,
    projections,
    databases,
    tables,
    used_dictionaries,
    exception_code,
    exception,
    log_comment,
    toString(normalized_query_hash)                        AS normalized_query_hash,
    query,
    ProfileEvents
FROM {sys.query_log}
WHERE query_id = {query_id}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
ORDER BY event_time_microseconds DESC
