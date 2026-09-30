SELECT
    time,
    avg(h_avg_merge)     AS avg_merge_pool_tasks,
    max(h_max_merge)     AS max_merge_pool_tasks,
    avg(h_avg_fetch)     AS avg_fetch_pool_tasks,
    max(h_max_fetch)     AS max_fetch_pool_tasks,
    avg(h_avg_schedule)  AS avg_schedule_pool_tasks,
    max(h_max_schedule)  AS max_schedule_pool_tasks,
    avg(h_avg_common)    AS avg_common_pool_tasks,
    max(h_max_common)    AS max_common_pool_tasks,
    -- Saturation is per replica: one node pinned at 16/16 among idle
    -- siblings averages out below any threshold in avg_fetch_pool_tasks.
    -- These are the max over replicas of each replica's hourly average —
    -- the grain the fetch_/schedule_pool_saturated alerts and the pre-pass
    -- use. On a single host they equal the avg_* columns.
    max(h_avg_fetch)     AS max_replica_avg_fetch_pool_tasks,
    max(h_avg_schedule)  AS max_replica_avg_schedule_pool_tasks,
    max(h_avg_merge)     AS max_replica_avg_merge_pool_tasks,
    avg(h_avg_isc)       AS avg_interserver_connections,
    sum(h_zk_tx)         AS zk_transactions,
    sum(h_zk_hw)         AS zk_hw_exceptions,
    avg(h_avg_mem)       AS avg_memory_tracking_bytes
FROM
(
    -- One row per replica per hour, aggregated on the replica that owns the
    -- rows; the outer query folds replicas. avg-of-avgs equals the flat
    -- average when every replica has a full hour of samples (metric_log is
    -- one row per second), and the max / sum columns are exact either way.
    SELECT
        hostName()                                              AS hostname,
        toStartOfHour(event_time)                               AS time,
        avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS h_avg_merge,
        max(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS h_max_merge,
        avg(CurrentMetric_BackgroundFetchesPoolTask)            AS h_avg_fetch,
        max(CurrentMetric_BackgroundFetchesPoolTask)            AS h_max_fetch,
        avg(CurrentMetric_BackgroundSchedulePoolTask)           AS h_avg_schedule,
        max(CurrentMetric_BackgroundSchedulePoolTask)           AS h_max_schedule,
        avg(CurrentMetric_BackgroundCommonPoolTask)             AS h_avg_common,
        max(CurrentMetric_BackgroundCommonPoolTask)             AS h_max_common,
        avg(CurrentMetric_InterserverConnection)                AS h_avg_isc,
        sum(ProfileEvent_ZooKeeperTransactions)                 AS h_zk_tx,
        sum(ProfileEvent_ZooKeeperHardwareExceptions)           AS h_zk_hw,
        avg(CurrentMetric_MemoryTracking)                       AS h_avg_mem
    FROM clusterAllReplicas(default, merge(system, '^metric_log'))
    WHERE event_time > {from:7d} AND event_time <= {to:now}
    GROUP BY hostname, time
)
GROUP BY time
ORDER BY time
