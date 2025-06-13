SELECT 
    time,
    query_kind,
    tables_salted AS tables,
    table,
    database,
    type,
    user,
    memory_usage,
    result_rows,
    result_bytes,
    written_bytes,
    written_rows,
    read_rows,
    read_bytes,
    interface,
    normalized_query_hash,
    count,
    minDate,
    maxDate,
    exception_code  
    FROM (
        SELECT
            toStartOfInterval(event_time, toIntervalHour(1)) AS time,
            query_kind,
            hex(SHA256(concat(splitByChar('.', tables)[1], '%salt%'))) as database,
            hex(SHA256(concat(splitByChar('.', tables)[2], '%salt%'))) as table,
            hex(SHA256(concat(tables, '%salt%'))) AS tables_salted,
            type,
            hex(SHA256(concat(user, '%salt%'))) AS user,
            sum(memory_usage) as memory_usage,
            sum(result_rows) as result_rows,
            sum(result_bytes) as result_bytes,
            sum(written_bytes) as written_bytes,
            sum(written_rows) as written_rows,
            sum(read_rows) as read_rows,
            sum(read_bytes) as read_bytes,
            interface,
            normalized_query_hash,
            count(*) as count,
            min(event_time) as minDate,
            max(event_time) as maxDate,
            exception_code
        FROM system.query_log
        ARRAY JOIN tables
        WHERE (event_time > (now() - toIntervalDay(15)))
        GROUP BY ALL
    )
FORMAT Native
