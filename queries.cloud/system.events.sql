-- tier: essential
-- 每个副本自启动以来的累计 ProfileEvents。
SELECT
    hostName() AS hostname,
    event,
    value
FROM clusterAllReplicas(default, system.events)
WHERE value > 0
ORDER BY event, hostname
