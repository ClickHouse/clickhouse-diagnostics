-- Background pools, Keeper and SharedMergeTree part-fetch counters per hour
-- PER HOST. metric_log_7_days folds the replicas into cluster-wide hourly
-- totals (plus the worst replica's pool average); this file keeps the host,
-- so a finding can be placed on one replica at one hour:
--
--   * which replica's fetch / schedule / merge pool is pinned at its size,
--     and what that size IS on that host (max(CurrentMetric_Background*PoolSize)
--     — the live limit, which steps up the hour a config reload raised it,
--     so a staged rollout is visible host by host; system.server_settings in
--     a cloud bundle comes from one replica only);
--   * Keeper request latency of each host — zk_wait_us / zk_transactions —
--     and which host a burst of hardware exceptions belongs to;
--   * on SharedMergeTree, how parts actually propagate: parts selected for
--     fetching per hour (SelectPartsFor*FetchParts) against fetches started
--     (DataPartsFetchAttempt, split into FromS3 / FromPeer) and the time
--     spent in them (HandleFetchPartsMicroseconds). The fetch executor has
--     no queue — a fetch that finds no free slot is dropped and re-selected
--     next round — so selected ≫ attempted is the saturation signature, and
--     attempted per hour flat at ≈ pool_size × 3600 / seconds-per-fetch on
--     every host is a pool working at its ceiling, not a stall. Leader
--     elections per hour (VirtualPartsUpdatesLeader*Election) size the
--     schedule-pool load that shared_merge_tree_leader_update_period_seconds
--     controls; ScheduleDataProcessingJob counts the rounds.
--   * CPU (OSCPUVirtualTimeMicroseconds, sum over threads — divide by 3600 s
--     × cores for a percentage) and PartsActive per host, so the catch-up
--     after a pool increase (fetch surge, then merges draining the small
--     parts) can be told apart from a node that stays hot.
--
-- Columns are selected two ways on purpose. The pool tasks, Keeper and CPU
-- counters exist on every supported version and are named explicitly. The
-- pool SIZES and the SharedMergeTree counters are picked by regex —
-- COLUMNS() yields nothing for a name this server does not have, never an
-- error — so one file serves Cloud, self-hosted SharedMergeTree (which
-- collects with this set via the onprem → cloud switch) and a plain
-- ReplicatedMergeTree cluster, where the SMT columns are simply absent or
-- zero. Output names for the regex columns are the raw
-- max(CurrentMetric_X) / sum(ProfileEvent_X), as in metric_log_coordination.
-- Verified: every name here exists in metric_log on 26.7; the election,
-- scheduling and fetch-handling counters were seen incrementing on a 26.6
-- SharedMergeTree service, the live pool sizes too.
--
-- Window: 3 days — one row per second per host in metric_log, and per-host
-- detail is for placing an incident; the week-long shape (weekday waves,
-- the ~63 h Keeper session renewals) is already in metric_log_7_days.
-- Rows: hosts × 72. Cloud only: on one node this is metric_log_7_days.
SELECT
    hostName()                                                 AS hostname,
    toStartOfHour(event_time)                                  AS time,
    avg(CurrentMetric_BackgroundFetchesPoolTask)               AS avg_fetch_pool_tasks,
    max(CurrentMetric_BackgroundFetchesPoolTask)               AS max_fetch_pool_tasks,
    avg(CurrentMetric_BackgroundSchedulePoolTask)              AS avg_schedule_pool_tasks,
    max(CurrentMetric_BackgroundSchedulePoolTask)              AS max_schedule_pool_tasks,
    avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask)    AS avg_merge_pool_tasks,
    max(CurrentMetric_BackgroundMergesAndMutationsPoolTask)    AS max_merge_pool_tasks,
    max(CurrentMetric_PartsActive)                             AS max_parts_active,
    sum(ProfileEvent_OSCPUVirtualTimeMicroseconds)             AS cpu_us,
    sum(ProfileEvent_ZooKeeperTransactions)                    AS zk_transactions,
    sum(ProfileEvent_ZooKeeperHardwareExceptions)              AS zk_hw_exceptions,
    sum(ProfileEvent_ZooKeeperWaitMicroseconds)                AS zk_wait_us,
    COLUMNS('^CurrentMetric_Background(Fetches|Schedule|MergesAndMutations)PoolSize$') APPLY max,
    COLUMNS('^ProfileEvent_SharedMergeTree(DataPartsFetchAttempt|DataPartsFetchFromS3|DataPartsFetchFromPeer|SelectPartsForRendezvousFetchParts|SelectPartsForCoordinatedFetchParts|HandleFetchPartsMicroseconds|VirtualPartsUpdatesLeaderSuccessfulElection|VirtualPartsUpdatesLeaderFailedElection|ScheduleDataProcessingJob)$') APPLY sum
FROM clusterAllReplicas(default, merge(system, '^metric_log'))
WHERE event_time > {from:3d} AND event_time <= {to:now}
GROUP BY hostname, time
ORDER BY hostname, time
