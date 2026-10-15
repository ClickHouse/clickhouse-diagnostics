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
--   * CPU (OSCPUVirtualTimeMicroseconds, summed over all threads in the
--     hour; for a complete hourly bucket the utilisation in percent is
--     cpu_us / (3 600 × 1e6 × cores) × 100 — a partial first or last bucket
--     needs its own seconds) and PartsActive per host, so the catch-up
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
    -- Local-disk I/O, so "is the disk the bottleneck?" is answerable from the
    -- bundle at all. A support case advised raising EBS throughput for slow
    -- merges off a screenshot the customer happened to send, because nothing
    -- here carried it. Every counter is summed over threads, so these are
    -- totals, not device measurements: OSRead*/OSWrite* Bytes are what the
    -- block device served (page cache excluded) — divided by covered_s, the
    -- hour's average traffic; the ReadBuffer*/WriteBuffer* pair is what
    -- ClickHouse asked for, so the two show how much the cache absorbed;
    -- Disk*ElapsedMicroseconds is thread time inside read/write calls, cache
    -- hits included and overlapping calls added up, so never a throughput
    -- denominator; OSIOWait is thread time blocked on storage, and
    -- OSIOWait / (covered_s * 1e6) is the average number of threads waiting.
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
    -- How much of the hour the samples cover: the rate denominator above,
    -- and restart evidence — an hour with fewer samples than its neighbours
    -- had downtime, and the first sample after a start can carry a counter's
    -- accumulated total (measured: 3.5e12 us of OSIOWait in one 1-second
    -- sample), so that hour's sums are not rates.
    count()                                                  AS metric_samples,
    dateDiff('second', min(event_time), max(event_time)) + 1 AS covered_s,
    COLUMNS('^CurrentMetric_Background(Fetches|Schedule|MergesAndMutations)PoolSize$') APPLY max,
    COLUMNS('^ProfileEvent_SharedMergeTree(DataPartsFetchAttempt|DataPartsFetchFromS3|DataPartsFetchFromPeer|SelectPartsForRendezvousFetchParts|SelectPartsForCoordinatedFetchParts|HandleFetchPartsMicroseconds|VirtualPartsUpdatesLeaderSuccessfulElection|VirtualPartsUpdatesLeaderFailedElection|ScheduleDataProcessingJob)$') APPLY sum
FROM clusterAllReplicas(default, merge(system, '^metric_log'))
WHERE event_time > {from:3d} AND event_time <= {to:now}
GROUP BY hostname, time
ORDER BY hostname, time
