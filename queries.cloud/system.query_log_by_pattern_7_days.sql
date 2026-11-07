-- tier: essential
-- 窗口内按查询模式（normalized_query_hash）、kind、用户和 type 统计，
-- 每个查询只计一次，并附带一条 SQL 样本。
-- dashboard 的最慢 / 最重模式以及按用户面板读取本文件。
-- timed_count 排除没有测量到持续时间的 QueryFinish 行。
-- hash 是 UInt64：以文本保存，这样 JavaScript 永远不会对它做舍入。
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
FROM clusterAllReplicas(default, system.query_log)
WHERE event_time > {from:7d} AND event_time <= {to:now}
  AND type != 'QueryStart'
GROUP BY hash, query_kind, user, type
