SELECT
    event_time,
    level,
    hex(SHA256(concat(logger_name, '%salt%'))) AS logger_name,
    hex(SHA256(concat(left(message, 500), '%salt%'))) AS message
FROM system.text_log
-- event_date prunes partitions; timezone() converts the window's endpoints
-- to the SERVER's calendar so pruning can't exclude rows near midnight.
-- event_time then bounds the window exactly — date-only filtering rounded
-- --from/--to out to whole days.
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  -- Critical sits between Fatal and Error in the level Enum8; "Warning and
  -- worse" is not complete without it.
  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
-- Severity first (the level Enum orders Fatal < Critical < Error < Warning),
-- then newest; at most 200 rows per (level, logger) so one chatty logger —
-- e.g. thousands of identical Warning lines a minute — cannot fill the cap
-- and hide the few Error lines that matter. LIMIT BY runs before LIMIT.
ORDER BY level ASC, event_time DESC
LIMIT 200 BY level, logger_name
LIMIT 2000
