-- tier: essential
-- 每个副本的 CurrentMetrics 快照。跨主机比较同一指标：
-- hostnames: a replica whose MetadataFromKeeperCacheObjects, ZooKeeperSession
-- 或 ReadonlyReplica 与同类不同的副本，就是需要关注的那个。
SELECT
    hostName() AS hostname,
    metric,
    value
FROM clusterAllReplicas(default, system.metrics)
ORDER BY metric, hostname
