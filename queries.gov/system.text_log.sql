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
  AND level IN ('Warning', 'Error', 'Fatal')
ORDER BY event_time DESC
LIMIT 2000
