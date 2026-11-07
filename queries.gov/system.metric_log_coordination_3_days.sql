-- 来自 metric_log 的每小时 Keeper / 对象存储 / 缓存 / 复制计数器，
-- 按列名正则选择，使文件能适配本版本导出的任何内容
-- （不存在的名称只是缺席 —— 绝不会报错）。
-- ProfileEvent_* 按小时求和，CurrentMetric_* 取每小时最大值。
-- 输出列命名为 sum(ProfileEvent_X) / max(CurrentMetric_Y)。
-- 与 metric_log_7_days 互补，后者保留固定的、跨版本稳定的集合。
-- 窗口：3 天。正则选择约 1700 个 metric_log 列中的 ~270 个，所以在繁忙
-- 服务器上做 7 天扫描，光事件时段就要读 >100 MiB 压缩数据；
-- 三天能保留基线，成本只有三分之一。
SELECT
    toStartOfHour(event_time) AS time,
    COLUMNS('^ProfileEvent_(ZooKeeper|Keeper|S3|DiskS3|ReadBufferFromS3|WriteBufferFromS3|AzureBlobStorage|ObjectStorage|FilesystemCache|CachedReadBuffer|ReplicatedPart|MergeTreeDataWriter|InsertedParts|FailedQuery|FailedInsertQuery|FailedSelectQuery|Query$|InsertQuery$|SelectQuery$|DelayedInserts|RejectedInserts|DistributedConnectionFail|Merge$|MergedRows|MergesTimeMilliseconds)') APPLY sum,
    COLUMNS('^CurrentMetric_(ZooKeeperSession|ZooKeeperWatch|ZooKeeperRequest|MemoryTracking|Query|ReadonlyReplica|ReplicatedFetch|ReplicatedSend|ReplicatedChecks|S3Requests|FilesystemCacheSize|FilesystemCacheElements|BackgroundSchedulePoolTask|BackgroundFetchesPoolTask|BackgroundMergesAndMutationsPoolTask|BackgroundCommonPoolTask|DelayedInserts|PartsActive|PartsOutdated|PartsDeleting|TCPConnection|HTTPConnection|InterserverConnection|DistributedFilesToInsert|BrokenDistributedFilesToInsert|Merge|Move)$') APPLY max
FROM system.metric_log
WHERE event_time > {from:3d} AND event_time <= {to:now}
GROUP BY time
ORDER BY time
