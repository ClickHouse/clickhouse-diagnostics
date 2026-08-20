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
    --any(query) as query,
    min(event_time) as minDate,
    max(event_time) as maxDate,
    exception_code,
    any(exception) as exception
FROM clusterAllReplicas(default, system.query_log)
ARRAY JOIN tables
WHERE (event_time > {from:7d} AND event_time <= {to:now})
GROUP BY ALL
