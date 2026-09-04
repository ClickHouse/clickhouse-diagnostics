SELECT
    toStartOfInterval(event_time, toIntervalHour(12)) AS time,
    event_type,
    merge_reason,
    partition_id,
    concat(database, '.', table) AS table_name,
    error,
    any(pl.exception) AS exception,
    uniqExact(pl.exception) AS distinct_exceptions,
    sum(peak_memory_usage) AS peak_memory_usage,
    sum(duration_ms) AS duration_ms,
    sum(size_in_bytes) AS size_in_bytes,
    count() as count
FROM system.part_log AS pl
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
-- distinct_exceptions is the denominator for any(exception). Because the
-- error code IS a group key, every row in a group shares it, so the sampled
-- message is representative rather than arbitrary — but the text still varies
-- with whatever identifier it embeds, and one message cannot say whether it
-- stands for 1 failure or 400 identical ones. uniqExact gives that in a single
-- integer, with no extra rows: 40 events / 1 distinct message is one recurring
-- fault, 40 / 40 is forty different ones that happen to share a code.
--
-- any()/uniqExact() read the column through the `pl.` table alias on purpose.
-- `any(exception) AS exception` puts `exception` in the query's alias scope,
-- and aliases are global, so a bare `uniqExact(exception)` resolves to that
-- aggregate rather than the column and the server rejects the query outright:
-- "Aggregate function any(exception) AS exception is found inside another
-- aggregate function" (ILLEGAL_AGGREGATION 184, hit on 26.7). Reordering the
-- projection does not help. Qualifying sidesteps the alias entirely; verified
-- on 22.8.21.38, 23.7.5.30 and 26.7.5.10.
--
-- Deliberately NOT a group key. Verified on 26.7: 8 UNKNOWN_DATABASE events
-- produced 8 distinct messages (each names its own database), and grouping on
-- the text turned 13 aggregated events into 13 rows — a row-for-row copy of
-- the source, the same trap part_name had.
WHERE (event_time > {from:7d} AND event_time <= {to:now})
-- Explicit key list instead of GROUP BY ALL: that syntax needs 22.12+
-- and this root file must run on every supported server (22.8+).
GROUP BY time, event_type, merge_reason, partition_id,
         table_name, error
