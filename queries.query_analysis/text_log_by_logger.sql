-- Per-logger breakdown of where wall-time was spent inside the query.
-- Useful to identify which subsystem (MergeTreeData, Aggregator,
-- ReadFromMergeTree, FilesystemCache, S3Client …) the query was waiting
-- on the longest.
-- Run when --query-id is set.
WITH (
    SELECT min(event_time_microseconds)
    FROM {sys.text_log}
    WHERE query_id = {query_id}
      AND event_time >= {from}
      AND event_time <= {to}
) AS query_start_time
SELECT
    min(event_time_microseconds)                                                   AS first_ts,
    logger_name,
    count()                                                                        AS message_count,
    dateDiff('millisecond', min(event_time_microseconds), max(event_time_microseconds)) AS time_spent_by_action_ms,
    dateDiff('millisecond', query_start_time, min(event_time_microseconds))        AS time_since_query_started_ms,
    any(level)                                                                     AS any_level,
    any(message)                                                                   AS sample_message
FROM {sys.text_log}
WHERE query_id = {query_id}
  AND event_time >= {from}
  AND event_time <= {to}
GROUP BY logger_name
ORDER BY time_spent_by_action_ms DESC
LIMIT 200
FORMAT Native
