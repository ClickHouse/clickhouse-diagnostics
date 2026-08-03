SELECT
    toStartOfInterval(event_time, toIntervalHour(12)) AS time,
    event_type,
    merge_reason,
    part_name,
    partition_id,
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%'))) AS table,
    error,
    sum(peak_memory_usage) AS peak_memory_usage,
    sum(duration_ms) AS duration_ms,
    sum(size_in_bytes) AS size_in_bytes,
    count() as count
FROM system.part_log
WHERE (event_time > (now() - toIntervalDay(7)))
-- Explicit key list instead of GROUP BY ALL: that syntax needs 22.12+
-- and this root file must run on every supported server (22.8+).
GROUP BY time, event_type, merge_reason, part_name, partition_id,
         database, table, error
FORMAT Native
