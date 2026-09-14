-- Point-in-time CurrentMetrics. The rows that matter in an incident:
-- ZooKeeperSession (0 = no Keeper session), ZooKeeperWatch/Request,
-- ReadonlyReplica, ReplicatedFetch/ReplicatedSend, S3Requests,
-- FilesystemCacheSize, MetadataFromKeeperCacheObjects (object storage with
-- metadata in Keeper), Background*PoolTask, DelayedInserts, MemoryTracking.
-- A snapshot: pair with metric_log_coordination_7_days for history.
SELECT
    metric,
    value,
    description
FROM system.metrics
ORDER BY metric
