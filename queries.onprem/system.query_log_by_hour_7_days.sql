-- tier: essential
-- Queries per hour, kind, type and error code, counted once per query —
-- no ARRAY JOIN (query_log_details_7_days repeats a query once per table it
-- touched). The dashboard's query-rate, per-kind, exception and Keeper-error
-- panels read this instead of querying system.query_log again.
SELECT
    toStartOfHour(event_time)                                    AS time,
    query_kind,
    type,
    exception_code,
    count()                                                      AS count,
    sum(query_duration_ms)                                       AS query_duration_ms,
    sum(memory_usage)                                            AS memory_usage,
    leftUTF8(anyIf(exception, exception_code != 0), 300)         AS exception
FROM system.query_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
  AND type != 'QueryStart'
GROUP BY time, query_kind, type, exception_code
ORDER BY time
