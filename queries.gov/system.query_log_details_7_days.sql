-- tier: essential
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
        -- 用 LEFT ARRAY JOIN 而不是 ARRAY JOIN：从未解析出表的查询会有
        -- 空的 `tables` 数组，普通 ARRAY JOIN 会把它丢掉 —— 而这恰恰是
        -- 值得归档的失败类别（UNKNOWN_TABLE、解析错误）。LEFT 会保留
        -- 它们；tables/database/table 随后哈希空字符串，这没问题。
        --
        -- 无论如何，触及 N 张表的查询会发出 N 行，每个聚合和都完整重复。
        -- 读取和之前先过滤到一张表；绝不要跨行再次求和。
        -- 这里有意不放原始查询文本：这是脱敏（gov）变体。
        -- 显式列出键，而不用 GROUP BY ALL：该语法需要
        -- 22.12+，而这个根目录文件必须能在所有受支持的
        -- 服务器（22.8+）上运行。键是内层非聚合投影。
        GROUP BY time, query_kind, database, table, tables_salted, type,
                 user, interface, normalized_query_hash, exception_code
    )
