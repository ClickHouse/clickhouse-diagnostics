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
    avg(CurrentMetric_InterserverConnection)                 AS avg_interserver_connections,
    sum(ProfileEvent_ZooKeeperTransactions)                  AS zk_transactions,
    sum(ProfileEvent_ZooKeeperHardwareExceptions)            AS zk_hw_exceptions,
    avg(CurrentMetric_MemoryTracking)                        AS avg_memory_tracking_bytes
FROM clusterAllReplicas(default, merge(system, '^metric_log'))
WHERE event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time
ORDER BY time
