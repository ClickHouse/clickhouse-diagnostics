-- Full query_log row for the focus query_id.
-- Run when --query-id is set.
--
-- Root variant for the oldest supported servers (22.8+). Columns that
-- arrived later are added by version overrides:
--   23.8.1.0/  → query_cache_usage      (added 23.8)
--   23.9.1.0/  → peak_threads_usage     (added 23.9)
--   23.11.1.0/ → hostname column        (added 23.11; root uses hostName())
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
FORMAT Native
