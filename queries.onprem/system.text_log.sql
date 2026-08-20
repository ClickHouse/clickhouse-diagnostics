SELECT
    event_time,
    level,
    logger_name,
    left(message, 500) AS message
FROM system.text_log
WHERE event_date >= toDate({from:1d}) AND event_date <= toDate({to:now})
  AND level IN ('Warning', 'Error', 'Fatal')
ORDER BY event_time DESC
LIMIT 2000
