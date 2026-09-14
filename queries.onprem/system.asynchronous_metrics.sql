-- Slow-updating server gauges: Uptime (the denominator for system.errors and
-- system.events), ReplicasMaxAbsoluteDelay, ReplicasSumQueueSize,
-- NumberOfTables/NumberOfDatabases, MaxPartCountForPartition,
-- TotalPartsOfMergeTreeTables, FilesystemCacheBytes, memory/RSS, jemalloc.
-- Per-core rows (…CPU12, …MHz_12) are dropped — they add hundreds of rows
-- and nothing a bundle reader needs.
SELECT
    metric,
    value,
    description
FROM system.asynchronous_metrics
WHERE NOT match(metric, '(CPU[0-9]+|MHz_[0-9]+)$')
ORDER BY metric
