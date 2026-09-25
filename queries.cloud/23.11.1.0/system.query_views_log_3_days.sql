-- What this collector answers:
--   * an MV that "succeeds" while writing nothing (read_rows > 0, written_rows = 0,
--     exception_code = 0) — the silent row-drop class. Invisible in query_log,
--     because the parent INSERT reports success.
--   * an MV push that threw while the base part was already committed, leaving the
--     base table and the MV target inconsistent (ExceptionWhileProcessing).
--   * per-view memory and duration over time — how you spot an upgrade that raises
--     one MV's peak memory several-fold while its row counts stay flat.
--   * divergence between two views in a chain, by comparing written_rows per bucket.
SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    -- hostname column added in 23.11; the root file has no host attribution.
    hostname,
    view_name,
    view_target,
    view_type,
    status,
    exception_code,
    count()                                             AS executions,
    -- NOT a fault on its own: an MV whose SELECT has a WHERE clause legitimately
    -- writes nothing for most blocks. That is why its denominator (executions) and
    -- the row totals sit on the same row — read it as a ratio per view over time.
    countIf(qvl.read_rows > 0 AND qvl.written_rows = 0) AS zero_write_executions,
    sum(qvl.read_rows)                                  AS read_rows,
    sum(qvl.read_bytes)                                 AS read_bytes,
    sum(qvl.written_rows)                               AS written_rows,
    sum(qvl.written_bytes)                              AS written_bytes,
    sum(qvl.view_duration_ms)                           AS view_duration_ms,
    max(qvl.view_duration_ms)                           AS max_view_duration_ms,
    -- sum() alone hides this class of regression: total memory tracks insert volume,
    -- so it moves for uninteresting reasons. The median/max pair against a flat
    -- written_rows is what identifies a single MV that got more expensive per block.
    avg(qvl.peak_memory_usage)                          AS avg_peak_memory_usage,
    quantile(0.5)(qvl.peak_memory_usage)                AS median_peak_memory_usage,
    max(qvl.peak_memory_usage)                          AS max_peak_memory_usage,
    -- Every aggregate argument is read through the `qvl.` alias on purpose.
    -- `sum(view_duration_ms) AS view_duration_ms` puts view_duration_ms in the
    -- query's GLOBAL alias scope, so a later bare max(view_duration_ms) resolves to
    -- that aggregate and the server rejects the whole query: "Aggregate function
    -- sum(view_duration_ms) AS view_duration_ms is found inside another aggregate
    -- function" (ILLEGAL_AGGREGATION 184, hit on 26.7.5.10 while writing this file).
    -- Qualifying sidesteps the alias entirely.
    leftUTF8(any(qvl.exception), 500)                   AS exception,
    min(qvl.event_time)                                 AS minDate,
    max(qvl.event_time)                                 AS maxDate
-- merge(), not system.query_views_log: a schema-changing upgrade renames the old
-- table to query_views_log_0. Reading only the live table silently loses the
-- pre-upgrade baseline — exactly the comparison a post-upgrade regression needs.
-- When no such table exists at all (log_query_views = 0, or <query_views_log> not
-- configured) merge() fails with code 636 CANNOT_EXTRACT_TABLE_STRUCTURE rather
-- than 60; that is a clean, recognisable "not collected" in execution_log.txt.
--
-- view_uuid is deliberately NOT collected: verified on 26.7.5.10 that ClickHouse
-- writes the zero UUID into it for every view (TO-target and inner-table alike),
-- while system.tables carries the real one. It would be a constant column. For an
-- MV without TO, view_target is `db.\`.inner_id.<uuid>\`` and DOES change when the
-- view is recreated, so that is the column to watch for a recreate.
FROM clusterAllReplicas(default, merge(system, '^query_views_log')) AS qvl
WHERE (event_time > {from:3d} AND event_time <= {to:now})
GROUP BY ALL
