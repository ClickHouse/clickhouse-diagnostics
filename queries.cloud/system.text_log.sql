SELECT
    event_time,
    level,
    logger_name,
    left(message, 500) AS message
FROM clusterAllReplicas(default, system.text_log)
WHERE event_date >= today() - 1
  AND level IN ('Warning', 'Error', 'Fatal')
ORDER BY event_time DESC
LIMIT 2000
