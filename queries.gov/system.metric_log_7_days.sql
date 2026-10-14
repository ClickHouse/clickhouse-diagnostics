SELECT
    toStartOfHour(event_time)           AS time,
    avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS avg_merge_pool_tasks,
    max(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS max_merge_pool_tasks,
    avg(CurrentMetric_BackgroundFetchesPoolTask)             AS avg_fetch_pool_tasks,
    -- Pool saturation is the finding a fetch-lag escalation turned on: the fetch
    -- pool at 16/16 and the schedule pool at 512/512 on every replica, read by
    -- hand from this table. max() beside avg() so a pool pinned at its size for
    -- the whole hour is told apart from one that touched it once. All four
    -- CurrentMetric_* exist on 22.8; the sizes are background_*_pool_size
    -- (system.settings below 23.3, system.server_settings from 23.3).
    max(CurrentMetric_BackgroundFetchesPoolTask)             AS max_fetch_pool_tasks,
    avg(CurrentMetric_BackgroundSchedulePoolTask)            AS avg_schedule_pool_tasks,
    max(CurrentMetric_BackgroundSchedulePoolTask)            AS max_schedule_pool_tasks,
    avg(CurrentMetric_BackgroundCommonPoolTask)              AS avg_common_pool_tasks,
    max(CurrentMetric_BackgroundCommonPoolTask)              AS max_common_pool_tasks,
    -- Same column set as the cloud file, where these are the max over
    -- replicas of each replica's hourly average; one host, so they equal avg_*.
    avg(CurrentMetric_BackgroundFetchesPoolTask)             AS max_replica_avg_fetch_pool_tasks,
    avg(CurrentMetric_BackgroundSchedulePoolTask)            AS max_replica_avg_schedule_pool_tasks,
    avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask)  AS max_replica_avg_merge_pool_tasks,
    avg(CurrentMetric_InterserverConnection)                 AS avg_interserver_connections,
    sum(ProfileEvent_ZooKeeperTransactions)                  AS zk_transactions,
    sum(ProfileEvent_ZooKeeperHardwareExceptions)            AS zk_hw_exceptions,
    -- Local-disk I/O, so "is the disk the bottleneck?" is answerable from the
    -- bundle at all. A support case advised raising EBS throughput for slow
    -- merges off a screenshot the customer happened to send, because nothing
    -- here carried it. Read as: bytes / elapsed = throughput,
    -- elapsed / operations = latency, and OSIOWait against the hour to see how
    -- much thread time went to waiting rather than working. OSRead*/OSWrite*
    -- are the block device with the page cache excluded, while the
    -- ReadBuffer*/WriteBuffer* pair is what ClickHouse asked for, so the two
    -- together show how much the cache absorbed.
    --
    -- Selected by regex rather than named: metric_log's column set moves
    -- between versions and one absent name fails the whole collector with code
    -- 47 — which is exactly how system.asynchronous_metrics broke on the 22.8
    -- floor. COLUMNS() yields nothing for a counter this server lacks.
    -- Measured cost on 226 663 rows (7 days): the collector reads 6.1 -> 13.0
    -- MiB and the file grows 20.8 -> 28.8 KiB, with no measurable change in
    -- wall time. No extra query and no extra round trip: the same scan, nine
    -- more of ~1700 columns.
    COLUMNS('^ProfileEvent_(ReadBufferFromFileDescriptorReadBytes|WriteBufferFromFileDescriptorWriteBytes|DiskReadElapsedMicroseconds|DiskWriteElapsedMicroseconds|OSRead(Bytes|Chars)|OSWrite(Bytes|Chars)|OSIOWaitMicroseconds)$') APPLY sum,
    avg(CurrentMetric_MemoryTracking)                        AS avg_memory_tracking_bytes
FROM system.metric_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time
ORDER BY time
