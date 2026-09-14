-- Per-replica server gauges (Uptime, replica delay/queue, part counts, cache
-- bytes, memory). Per-core rows are dropped.
SELECT
    hostName() AS hostname,
    metric,
    value
FROM clusterAllReplicas(default, system.asynchronous_metrics)
WHERE NOT match(metric, '(CPU[0-9]+|MHz_[0-9]+)$')
ORDER BY metric, hostname
