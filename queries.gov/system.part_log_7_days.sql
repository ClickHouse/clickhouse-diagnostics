SELECT
    toStartOfInterval(event_time, toIntervalHour(12)) AS time,
    event_type,
    merge_reason,
    partition_id,
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%'))) AS table,
    error,
    sum(peak_memory_usage) AS peak_memory_usage,
    sum(duration_ms) AS duration_ms,
    sum(size_in_bytes) AS size_in_bytes,
    count() as count
FROM system.part_log
-- part_name is deliberately NOT collected at all, in either form.
--
-- As a GROUP BY key it is fatal: it is unique per part, so keying on it turns
-- this "aggregate" into a row-for-row copy of system.part_log (868 rows for
-- 868 events on a toy dataset; millions over 7 days of production). That is
-- the regression this file exists to avoid, so do not add it back as a key.
--
-- Sampled with any(part_name) it is merely useless: one arbitrary part out of
-- however many the group covers, with nothing to say why that one was picked.
-- It is not a handle you can troubleshoot from. The useful grain is
-- bucket x table x event_type x merge_reason x partition x error, and
-- partition_id is the identifier to follow up on.
WHERE (event_time > {from:7d} AND event_time <= {to:now})
-- Explicit key list instead of GROUP BY ALL: that syntax needs 22.12+
-- and this root file must run on every supported server (22.8+).
GROUP BY time, event_type, merge_reason, partition_id,
         database, table, error
