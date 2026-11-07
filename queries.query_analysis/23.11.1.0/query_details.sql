-- 23.11+ 变体：hostname 列 + query_cache_usage（23.8）+ peak_threads_usage（23.9）。22.8 基线见根文件。
-- 目标 query_id 的完整 query_log 行。
-- 当设置了 --query-id 时运行。
SELECT
    event_time_microseconds                                AS ts,
    query_id,
    query_kind,
    type,
    user,
    initial_user,
    is_initial_query,
    hostname,
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
    peak_threads_usage,
    projections,
    databases,
    tables,
    used_dictionaries,
    query_cache_usage,
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
