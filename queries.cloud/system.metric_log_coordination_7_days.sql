-- Hourly Keeper / object-storage / cache / replication counters from metric_log,
-- selected by column-name regex so the file adapts to whatever this version
-- exports (a name that does not exist is simply absent — never an error).
-- ProfileEvent_* are summed per hour, CurrentMetric_* take the hourly max.
-- Output columns are named sum(ProfileEvent_X) / max(CurrentMetric_Y).
-- Complements metric_log_7_days, which keeps a fixed, version-stable set.
-- Cloud: summed / maxed across all replicas, like metric_log_7_days.
SELECT
    toStartOfHour(event_time) AS time,
    COLUMNS('^ProfileEvent_(ZooKeeper|Keeper|S3|DiskS3|ReadBufferFromS3|WriteBufferFromS3|AzureBlobStorage|ObjectStorage|FilesystemCache|CachedReadBuffer|ReplicatedPart|MergeTreeDataWriter|InsertedParts|FailedQuery|FailedInsertQuery|FailedSelectQuery|Query$|InsertQuery$|SelectQuery$|DelayedInserts|RejectedInserts|DistributedConnectionFail|Merge$|MergedRows|MergesTimeMilliseconds)') APPLY sum,
    COLUMNS('^CurrentMetric_(ZooKeeperSession|ZooKeeperWatch|ZooKeeperRequest|MemoryTracking|Query|ReadonlyReplica|ReplicatedFetch|ReplicatedSend|ReplicatedChecks|S3Requests|FilesystemCacheSize|FilesystemCacheElements|BackgroundSchedulePoolTask|BackgroundFetchesPoolTask|BackgroundMergesAndMutationsPoolTask|BackgroundCommonPoolTask|DelayedInserts|PartsActive|PartsOutdated|PartsDeleting|TCPConnection|HTTPConnection|InterserverConnection|DistributedFilesToInsert|BrokenDistributedFilesToInsert|Merge|Move)$') APPLY max
FROM clusterAllReplicas(default, merge(system, '^metric_log'))
WHERE event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time
ORDER BY time
