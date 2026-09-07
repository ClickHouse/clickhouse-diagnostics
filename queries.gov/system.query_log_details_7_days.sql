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
    query_duration_ms,
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
            sum(query_duration_ms) as query_duration_ms,
            interface,
            normalized_query_hash,
            count(*) as count,
            min(event_time) as minDate,
            max(event_time) as maxDate,
            exception_code
        FROM system.query_log
        LEFT ARRAY JOIN tables
        WHERE (event_time > {from:7d} AND event_time <= {to:now})
        -- LEFT ARRAY JOIN, not ARRAY JOIN: a query that never resolved a table has
        -- an empty `tables` array and a plain ARRAY JOIN drops it — exactly the
        -- failure class worth archiving (UNKNOWN_TABLE, parse errors). LEFT keeps
        -- them; tables/database/table then hash the empty string, which is fine.
        --
        -- Either way, a query touching N tables emits N rows with every sum repeated
        -- in full. Filter to one table before reading sums; never re-sum across rows.
        -- No raw query text here by design: this is the redacted (gov) variant.
        -- Explicit key list instead of GROUP BY ALL: that syntax needs
        -- 22.12+ and this root file must run on every supported server
        -- (22.8+). Keys are the inner non-aggregate projections.
        GROUP BY time, query_kind, database, table, tables_salted, type,
                 user, interface, normalized_query_hash, exception_code
    )
