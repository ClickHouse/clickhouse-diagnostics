-- tier: essential
-- Per query pattern (normalized_query_hash), kind, user and type over the
-- window, counted once per query, with one sample of the SQL. The
-- dashboard's slowest / heaviest patterns and per-user panels read this.
-- timed_count leaves out the QueryFinish rows with no measured duration.
-- The hash is a UInt64: kept as text so JavaScript never rounds it.
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
    leftUTF8(any(query), 600)                      AS sample_query
FROM clusterAllReplicas(default, system.query_log)
WHERE event_time > {from:7d} AND event_time <= {to:now}
  AND type != 'QueryStart'
GROUP BY hash, query_kind, user, type
