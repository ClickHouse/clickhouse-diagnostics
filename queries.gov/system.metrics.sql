-- tier: essential
-- 时间点 CurrentMetrics。事件中重要的行：
-- ZooKeeperSession（0 = 无 Keeper 会话）、ZooKeeperWatch/Request、
-- ReadonlyReplica、ReplicatedFetch/ReplicatedSend、S3Requests、
-- FilesystemCacheSize、MetadataFromKeeperCacheObjects（元数据在 Keeper
-- 中的对象存储）、Background*PoolTask、DelayedInserts、MemoryTracking。
-- 快照：结合 metric_log_coordination_3_days 查看历史。
SELECT
    metric,
    value,
    description
FROM system.metrics
ORDER BY metric
