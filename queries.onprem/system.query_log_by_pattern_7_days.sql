-- tier: essential
-- 按查询模式（normalized_query_hash）、kind、user 和 type 在窗口内
-- 聚合，每条查询只计一次，并附带一条 SQL 样本。
-- dashboard 的 slowest / heaviest 模式和 per-user 面板都读取此文件。
-- timed_count 排除没有测得耗时的 QueryFinish 行。
-- hash 是 UInt64：以文本形式保留，以免 JavaScript 对其进行舍入。
SELECT
    toString(normalized_query_hash)                AS hash,
    query_kind,
    user,
    type,
    count()                                        AS count,
    countIf(query_duration_ms > 0)                 AS timed_count,
    sum(query_duration_ms)                         AS total_duration_ms,
    max(query_duration_ms)                         AS max_duration_ms,
    sum(read_bytes)                                AS total_read_bytes,
    sum(memory_usage)                              AS total_memory_usage,
    sumIf(read_bytes, query_duration_ms > 0)       AS timed_read_bytes,
    sumIf(memory_usage, query_duration_ms > 0)     AS timed_memory_usage,
    leftUTF8(any(query), 600)                      AS sample_query
FROM system.query_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
  AND type != 'QueryStart'
GROUP BY hash, query_kind, user, type
