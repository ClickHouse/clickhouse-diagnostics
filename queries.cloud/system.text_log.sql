SELECT
    event_time,
    level,
    logger_name,
    -- leftUTF8, not left: left() counts bytes, so a cut inside a
    -- multi-byte character leaves invalid UTF-8 in the .jsonl output.
    leftUTF8(message, 500) AS message
FROM clusterAllReplicas(default, system.text_log)
-- event_date prunes partitions; timezone() converts the window's endpoints
-- to the SERVER's calendar so pruning can't exclude rows near midnight.
-- event_time then bounds the window exactly — date-only filtering rounded
-- --from/--to out to whole days.
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  AND level IN ('Warning', 'Error', 'Fatal')
ORDER BY event_time DESC
LIMIT 2000
