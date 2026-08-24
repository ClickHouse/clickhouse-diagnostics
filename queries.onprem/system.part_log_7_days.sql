SELECT
    toStartOfInterval(event_time, toIntervalHour(12)) AS time,
    event_type,
    merge_reason,
    part_name,
    partition_id,
    concat(database, '.', table) AS table_name,
    error,
    any(exception),
    sum(peak_memory_usage) AS peak_memory_usage,
    sum(duration_ms) AS duration_ms,
    sum(size_in_bytes) AS size_in_bytes,
    count() as count
FROM system.part_log
WHERE (event_time > {from:7d} AND event_time <= {to:now})
-- Explicit key list instead of GROUP BY ALL: that syntax needs 22.12+
-- and this root file must run on every supported server (22.8+).
GROUP BY time, event_type, merge_reason, part_name, partition_id,
         table_name, error
