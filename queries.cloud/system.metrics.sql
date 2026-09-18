-- Per-replica CurrentMetrics snapshot. Compare the same metric across
-- hostnames: a replica whose MetadataFromKeeperCacheObjects, ZooKeeperSession
-- or ReadonlyReplica differs from its peers is the one to look at.
SELECT
    hostName() AS hostname,
    metric,
    value
FROM clusterAllReplicas(default, system.metrics)
ORDER BY metric, hostname
