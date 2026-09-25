-- What this collector answers:
--   * an MV that "succeeds" while writing nothing (read_rows > 0, written_rows = 0,
--     exception_code = 0) — the silent row-drop class. Invisible in query_log,
--     because the parent INSERT reports success.
--   * an MV push that threw while the base part was already committed, leaving the
--     base table and the MV target inconsistent (ExceptionWhileProcessing).
--   * per-view memory and duration over time — how you spot an upgrade that raises
--     one MV's peak memory several-fold while its row counts stay flat.
--   * divergence between two views in a chain, by comparing written_rows per bucket.
--
-- Redacted (gov) variant. Two differences from the onprem file:
--   * no exception text. An MV failure message embeds the identifier it failed on
--     ("Unknown table expression identifier 'db.lookup'"), so only the numeric
--     exception_code is collected — the same rule part_log and query_log follow.
--   * view_name and view_target are 'database.table' strings and are split before
--     hashing. PrintGovNameMapping builds the reversal CSV from system.tables, so
--     it keys on database and table names SEPARATELY; a hash of the joined string
--     matches nothing in the mapping and is unreadable to the analyst.
--
-- Hashing sits in the OUTER select, over the aggregated result, so SHA256 runs once
-- per output row rather than once per source row.
--
-- view_uuid is deliberately NOT collected: verified on 26.7.5.10 that ClickHouse
-- writes the zero UUID into it for every view, while system.tables carries the
-- real one. It would be a constant column.
SELECT
    time,
    hex(SHA256(concat(splitByChar('.', view_name)[1], '%salt%')))   AS view_database,
    hex(SHA256(concat(splitByChar('.', view_name)[2], '%salt%')))   AS view_table,
    hex(SHA256(concat(splitByChar('.', view_target)[1], '%salt%'))) AS target_database,
    hex(SHA256(concat(splitByChar('.', view_target)[2], '%salt%'))) AS target_table,
    view_type,
    status,
    exception_code,
    executions,
    zero_write_executions,
    read_rows,
    read_bytes,
    written_rows,
    written_bytes,
    view_duration_ms,
    max_view_duration_ms,
    avg_peak_memory_usage,
    median_peak_memory_usage,
    max_peak_memory_usage,
    minDate,
    maxDate
FROM (
    SELECT
        toStartOfInterval(event_time, toIntervalHour(1)) AS time,
        view_name,
        view_target,
        view_type,
        status,
        exception_code,
        count()                                             AS executions,
        -- NOT a fault on its own: an MV whose SELECT has a WHERE clause legitimately
        -- writes nothing for most blocks. Read it as a ratio of executions.
        countIf(qvl.read_rows > 0 AND qvl.written_rows = 0) AS zero_write_executions,
        sum(qvl.read_rows)                                  AS read_rows,
        sum(qvl.read_bytes)                                 AS read_bytes,
        sum(qvl.written_rows)                               AS written_rows,
        sum(qvl.written_bytes)                              AS written_bytes,
        sum(qvl.view_duration_ms)                           AS view_duration_ms,
        max(qvl.view_duration_ms)                           AS max_view_duration_ms,
        -- Every aggregate argument is read through the `qvl.` alias on purpose:
        -- `sum(view_duration_ms) AS view_duration_ms` puts the name in the query's
        -- GLOBAL alias scope, so a later bare max(view_duration_ms) resolves to that
        -- aggregate and the server rejects the query (ILLEGAL_AGGREGATION 184).
        avg(qvl.peak_memory_usage)                          AS avg_peak_memory_usage,
        quantile(0.5)(qvl.peak_memory_usage)                AS median_peak_memory_usage,
        max(qvl.peak_memory_usage)                          AS max_peak_memory_usage,
        min(qvl.event_time)                                 AS minDate,
        max(qvl.event_time)                                 AS maxDate
    FROM merge(system, '^query_views_log') AS qvl
    WHERE (event_time > {from:3d} AND event_time <= {to:now})
    -- Explicit key list instead of GROUP BY ALL: that syntax needs 22.12+ and this
    -- root file must run on every supported server (22.8+).
    GROUP BY time, view_name, view_target, view_type, status, exception_code
)
