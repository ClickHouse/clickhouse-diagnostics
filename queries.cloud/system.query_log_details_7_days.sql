SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    query_kind,
    tables,
    splitByChar('.', tables)[1] as database,
    splitByChar('.', tables)[2] as table,
    type,
    user,
    sum(memory_usage) as memory_usage,
    sum(result_rows) as result_rows,
    sum(result_bytes) as result_bytes,
    sum(written_bytes) as written_bytes,
    sum(written_rows) as written_rows,
    sum(read_rows) as read_rows,
    sum(read_bytes) as read_bytes,
    sum(query_duration_ms) as query_duration_ms,
    interface,
    normalized_query_hash,
    count(*) as count,
    -- Truncated so the archive stays small, but present: without it a
    -- normalized_query_hash in this file cannot be mapped back to SQL
    -- once the server is gone.
    --
    -- leftUTF8, not left: left() counts BYTES, so a cut landing inside a
    -- multi-byte character emits a lone continuation byte and the .jsonl
    -- line stops being valid UTF-8 — the server's response is written to
    -- disk verbatim (internal/query/executor.go), so nothing downstream
    -- repairs it. Strict parsers then reject the line outright
    -- (python json: "invalid continuation byte"); jq is lenient and
    -- silently substitutes U+FFFD, which corrupts the text just as much
    -- but quietly. Reachable with any non-ASCII literal in customer SQL
    -- ('München', CJK identifiers). leftUTF8 counts code points and is
    -- registered alongside left in 22.8, so it needs no version gate.
    leftUTF8(any(query), 500) as query,
    min(event_time) as minDate,
    max(event_time) as maxDate,
    exception_code,
    any(ql.exception) as exception,
    uniqExact(ql.exception) as distinct_exceptions
FROM clusterAllReplicas(default, system.query_log) AS ql
LEFT ARRAY JOIN tables
-- distinct_exceptions is the denominator for any(exception). Because the
-- error code IS a group key, every row in a group shares it, so the sampled
-- message is representative rather than arbitrary — but the text still varies
-- with whatever identifier it embeds, and one message cannot say whether it
-- stands for 1 failure or 400 identical ones. uniqExact gives that in a single
-- integer, with no extra rows: 40 events / 1 distinct message is one recurring
-- fault, 40 / 40 is forty different ones that happen to share a code.
--
-- any()/uniqExact() read the column through the `ql.` table alias on purpose.
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
-- LEFT ARRAY JOIN, not ARRAY JOIN: a query that never resolved a table has an
-- empty `tables` array, and a plain ARRAY JOIN drops those rows entirely. That
-- is exactly the failure class worth archiving — UNKNOWN_TABLE (60), parse
-- errors (6/27), anything that threw before analysis finished. LEFT keeps them
-- with tables = ''.
--
-- Consequence of the array join, either way: a query touching N tables emits N
-- rows and EVERY sum is repeated in full on each one. Filter to a single table
-- before reading the sums; never re-sum across rows or you will multiply-count.
GROUP BY ALL
