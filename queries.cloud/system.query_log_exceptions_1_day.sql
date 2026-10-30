-- tier: essential
-- Failed queries per hour and error code, counted once per query.
-- query_log_details_7_days repeats a query once per table it touched (LEFT
-- ARRAY JOIN tables), so a count read off it is either doubled or, de-
-- duplicated, merges two executions of one pattern on different tables;
-- the exception alerts (high_exception_rate, keeper_exception_spike,
-- too_many_simultaneous_queries) read this exact count instead. Only failed
-- rows are read; one day covers their 24-hour look-back.
SELECT
    toStartOfHour(event_time)  AS time,
    type,
    exception_code,
    count()                    AS count,
    any(exception)             AS exception
FROM clusterAllReplicas(default, system.query_log)
WHERE event_time > {from:1d} AND event_time <= {to:now}
  AND exception_code != 0
GROUP BY time, type, exception_code
ORDER BY time, exception_code
