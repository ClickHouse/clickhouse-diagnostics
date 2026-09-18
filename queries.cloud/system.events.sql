-- Per-replica cumulative ProfileEvents since each replica's start.
SELECT
    hostName() AS hostname,
    event,
    value
FROM clusterAllReplicas(default, system.events)
WHERE value > 0
ORDER BY event, hostname
