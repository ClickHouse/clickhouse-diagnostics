-- tier: essential
-- 更新缓慢的服务器指标：Uptime（system.errors 与 system.events 的分母）、
-- ReplicasMaxAbsoluteDelay、ReplicasSumQueueSize、
-- NumberOfTables/NumberOfDatabases、MaxPartCountForPartition、
-- TotalPartsOfMergeTreeTables、FilesystemCacheBytes、内存/RSS、jemalloc。
-- 按核心拆分的行（…CPU12、…MHz_12）被丢弃——它们会增加数百行，
-- 而 bundle 读取者并不需要。
SELECT
    metric,
    value,
    description
FROM system.asynchronous_metrics
WHERE NOT match(metric, '(CPU[0-9]+|MHz_[0-9]+)$')
ORDER BY metric
