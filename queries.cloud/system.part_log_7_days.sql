SELECT
    toStartOfInterval(event_time, toIntervalHour(12)) AS time,
    event_type,
    merge_reason,
    any(part_name) AS part_name,
    partition_id,
    concat(database, '.', table) AS table_name,
    error,
    any(exception) AS exception,
    sum(peak_memory_usage) AS peak_memory_usage,
    sum(duration_ms) AS duration_ms,
    sum(size_in_bytes) AS size_in_bytes,
    count() as count
FROM clusterAllReplicas(default, system.part_log)
-- part_name is deliberately NOT a group key: it is unique per part, so keying
-- on it makes this "aggregate" a row-for-row copy of system.part_log (868 rows
-- for 868 events on a toy dataset; millions over 7 days of production). The
-- useful grain is bucket x table x event_type x merge_reason x partition x
-- error; any(part_name) keeps one example per group for follow-up lookups.
WHERE (event_time > {from:7d} AND event_time <= {to:now})
GROUP BY ALL
