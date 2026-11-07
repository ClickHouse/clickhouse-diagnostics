-- tier: essential
-- 某一时刻的 CurrentMetrics。事故中值得关注的指标行：
-- ZooKeeperSession（0 = 没有 Keeper 会话）、ZooKeeperWatch/Request、
-- ReadonlyReplica、ReplicatedFetch/ReplicatedSend、S3Requests、
-- FilesystemCacheSize、MetadataFromKeeperCacheObjects（元数据放在
-- Keeper 中的对象存储）、Background*PoolTask、DelayedInserts、MemoryTracking。
-- 这是一份快照：需要历史时配合 metric_log_coordination_3_days 使用。
SELECT
    metric,
    value,
    description
FROM system.metrics
ORDER BY metric
