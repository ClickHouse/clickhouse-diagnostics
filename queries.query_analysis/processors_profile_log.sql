-- Plan-step level timings for the focus query_id.
-- Each row corresponds to a processor in the query's execution
-- pipeline; elapsed_ms is how long it actively ran, wait_ms is how
-- long it sat blocked on upstream input. Use this to find the
-- bottleneck step (e.g. an Aggregator stuck waiting on a sort).
-- Run when --query-id is set.
SELECT
    hex(plan_step)                                          AS plan_step,
    name,
    round(sum(elapsed_us) / 1000, 1)                        AS elapsed_ms,
    round(sum(input_wait_elapsed_us) / 1000, 1)             AS wait_ms,
    sum(input_rows)                                         AS input_rows,
    sum(output_rows)                                        AS output_rows,
    sum(input_bytes)                                        AS input_bytes,
    sum(output_bytes)                                       AS output_bytes,
    count()                                                 AS processor_count
FROM {sys.processors_profile_log}
WHERE query_id = {query_id}
  AND event_time >= {from}
  AND event_time <= {to}
GROUP BY plan_step, name
ORDER BY elapsed_ms DESC
LIMIT 200
FORMAT Native
