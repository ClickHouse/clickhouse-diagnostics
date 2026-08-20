SELECT
    event_time,
    level,
    hex(SHA256(concat(logger_name, '%salt%'))) AS logger_name,
    hex(SHA256(concat(left(message, 500), '%salt%'))) AS message
FROM system.text_log
WHERE event_date >= toDate({from:1d}) AND event_date <= toDate({to:now})
  AND level IN ('Warning', 'Error', 'Fatal')
ORDER BY event_time DESC
LIMIT 2000
