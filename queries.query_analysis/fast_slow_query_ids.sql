-- The two representative query_ids for the focus hash: the slowest
-- execution (argMax by duration) and the fastest (argMin). These
-- become the inputs to profile_events_compare.sql.
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
-- toString(normalized_query_hash) is aliased to *_str so the alias does
-- not shadow the source UInt64 column in the WHERE clause (CH 25.12's
-- analyzer otherwise resolves `normalized_query_hash = 12345` against
-- the String alias → NO_COMMON_TYPE).
SELECT
    toString(any(normalized_query_hash))                    AS normalized_query_hash_str,
    argMax(query_id, query_duration_ms)                     AS slow_query_id,
    argMin(query_id, query_duration_ms)                     AS fast_query_id,
    toString(argMax(event_time, query_duration_ms))         AS slow_event_time,
    toString(argMin(event_time, query_duration_ms))         AS fast_event_time,
    max(query_duration_ms)                                  AS slow_duration_ms,
    min(query_duration_ms)                                  AS fast_duration_ms,
    count()                                                 AS executions
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type = 'QueryFinish'
  AND NOT has(databases, 'system')
FORMAT Native
