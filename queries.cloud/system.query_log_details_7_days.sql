SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    query_kind,
    tables,
    splitByChar('.', tables)[1] as database,
    splitByChar('.', tables)[2] as table,
    type,
    user,
    sum(memory_usage) as memory_usage,
    sum(result_rows) as result_rows,
    sum(result_bytes) as result_bytes,
    sum(written_bytes) as written_bytes,
    sum(written_rows) as written_rows,
    sum(read_rows) as read_rows,
    sum(read_bytes) as read_bytes,
    sum(query_duration_ms) as query_duration_ms,
    interface,
    normalized_query_hash,
    count(*) as count,
    -- Truncated so the archive stays small, but present: without it a
    -- normalized_query_hash in this file cannot be mapped back to SQL
    -- once the server is gone.
    left(any(query), 500) as query,
    min(event_time) as minDate,
    max(event_time) as maxDate,
    exception_code,
    any(exception) as exception
FROM clusterAllReplicas(default, system.query_log)
LEFT ARRAY JOIN tables
WHERE (event_time > {from:7d} AND event_time <= {to:now})
-- LEFT ARRAY JOIN, not ARRAY JOIN: a query that never resolved a table has an
-- empty `tables` array, and a plain ARRAY JOIN drops those rows entirely. That
-- is exactly the failure class worth archiving — UNKNOWN_TABLE (60), parse
-- errors (6/27), anything that threw before analysis finished. LEFT keeps them
-- with tables = ''.
--
-- Consequence of the array join, either way: a query touching N tables emits N
-- rows and EVERY sum is repeated in full on each one. Filter to a single table
-- before reading the sums; never re-sum across rows or you will multiply-count.
GROUP BY ALL
