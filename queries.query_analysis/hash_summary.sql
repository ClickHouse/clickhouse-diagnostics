-- Per-minute execution count for the focus hash. The dashboard's
-- count chart used to bucket by hour, but that hid the within-hour
-- spread of executions; per-minute bucketing gives finer granularity
-- without exploding the row count for typical investigation windows.
--
-- Value-based metrics (duration, memory, user CPU, read rows/bytes)
-- are now driven from executions_timeline.sql instead — one row per
-- execution rather than aggregated. So hash_summary now only carries
-- the count split (succeeded vs failed), which is the one chart
-- where per-execution doesn't make sense.
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
SELECT
    toString(toStartOfMinute(event_time))                   AS time_bucket,
    count()                                                 AS executions,
    countIf(type = 'QueryFinish')                           AS succeeded,
    countIf(type != 'QueryFinish' AND type != 'QueryStart') AS failed
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
GROUP BY time_bucket
ORDER BY time_bucket
FORMAT Native
