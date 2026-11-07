-- tier: essential
-- 每小时按主机统计的后台 pool、Keeper 和 SharedMergeTree part-fetch 计数器
-- 按主机。metric_log_7_days 会把副本折叠成集群范围的小时
-- 总量（加上最差副本的 pool 平均值）；本文件保留主机，
-- 因此可以把一个 finding 定位到某个副本的某个小时：
--
--   * 哪个副本的 fetch / schedule / merge pool 已顶到其容量，
--     以及该主机上的容量“是多少”（max(CurrentMetric_Background*PoolSize)
--     ——实时上限，配置重载提高它时会在该小时跳升，
--     因此分阶段发布可以逐主机可见；cloud bundle 中的 system.server_settings
--     只来自一个副本）；
--   * 每台主机的 Keeper 请求延迟——zk_wait_us / zk_transactions——
--     以及一串硬件异常属于哪台主机；
--   * 在 SharedMergeTree 上，part 实际如何传播：每小时为
--     fetch 选中的 part（SelectPartsFor*FetchParts）与已启动的 fetch
--     （DataPartsFetchAttempt，分为 FromS3 / FromPeer）以及花费在
--     其中的时间（HandleFetchPartsMicroseconds）。fetch 执行器
--     没有队列——找不到空闲槽位的 fetch 会被丢弃并在下一轮
--     重新选中——因此“选中数 ≫ 尝试数”就是饱和特征，
--     而每台主机上每小时“尝试数”稳定在约 pool_size × 3600 / 每次 fetch 的秒数
--     说明 pool 正以极限工作，而不是停滞。每小时
--     leader election（VirtualPartsUpdatesLeader*Election）衡量
--     shared_merge_tree_leader_update_period_seconds 控制的
--     schedule-pool 负载；ScheduleDataProcessingJob 统计轮数。
--   * CPU（OSCPUVirtualTimeMicroseconds，按该小时所有线程求和；
--     对于完整的小时 bucket，利用率的百分比为
--     cpu_us / (3 600 × 1e6 × cores) × 100——不完整的首尾 bucket
--     需要它自己的秒数）以及每台主机的 PartsActive，这样就能把
--     pool 扩容后的追赶（fetch 激增，随后 merge 消化小
--     part）与持续高负载的节点区分开。
--
-- 有意用两种方式选择列。pool 任务、Keeper 和 CPU
-- 计数器在每个受支持版本中都存在，并显式命名。pool
-- 容量和 SharedMergeTree 计数器用正则选择——
-- COLUMNS() 对服务器没有的名称返回空，绝不会
-- 报错——因此一个文件同时服务于 Cloud、自托管 SharedMergeTree（它
-- 通过 onprem → cloud 切换使用这组采集器）和普通
-- ReplicatedMergeTree 集群，后者中 SMT 列只是缺失或
-- 为零。正则列的输出名称是原始的
-- max(CurrentMetric_X) / sum(ProfileEvent_X)，与 metric_log_coordination 中一样。
-- 已验证：这里的每个名称都存在于 26.7 的 metric_log 中；election、
-- scheduling 和 fetch-handling 计数器在 26.6
-- SharedMergeTree 服务上观察到递增，实时 pool 容量也是如此。
--
-- 窗口：3 天——metric_log 中每台主机每秒一行，按主机
-- 细节用于定位事故；一周的形态（工作日波动、
-- 约 63 小时的 Keeper 会话续期）已经在 metric_log_7_days 中。
-- 行数：主机数 × 72。仅 Cloud：在单节点上这就是 metric_log_7_days。
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
    -- 本地磁盘 I/O，这样“磁盘是瓶颈吗？”才能从
    -- bundle 中得到回答。一个支持案例曾根据客户随手发来的截图，
    -- 建议为缓慢的 merge 提高 EBS 吞吐量，因为这里没有任何数据
    -- 能反映它。每个计数器都按线程求和，所以这些是
    -- 总量，不是设备测量值：OSRead*/OSWrite* Bytes 是
    -- 块设备实际服务的数据（不含 page cache（页缓存））——按覆盖秒数计，是
    -- 该小时的平均流量；ReadBuffer*/WriteBuffer* 这一对是
    -- ClickHouse 请求的量，因此两者显示缓存吸收了多少；
    -- Disk*ElapsedMicroseconds 是读写调用内部的线程时间，包含
    -- 缓存命中且重叠调用会相加，因此绝不能作为吞吐量
    -- 分母；OSIOWait 是阻塞在存储上的线程时间，按
    -- 覆盖秒数计就是平均等待线程数。OS
    -- 计数器覆盖所有本地卷：它们说明服务器存储有多忙，
    -- 而不是哪个卷。
    --
    -- 用正则选择而不是显式命名：metric_log 的列集合会在
    -- 版本间变化，一个缺失名称就会让整个 collector 以错误码
    -- 47 失败——这正是 system.asynchronous_metrics 在 22.8
    -- 下限上出问题的方式。COLUMNS() 对服务器缺少的计数器返回空。
    -- 在 226 663 行（7 天）上测得开销：collector 读取 6.1 -> 13.0
    -- MiB，文件增长 20.8 -> 28.8 KiB，实际耗时没有可测量的
    -- 变化。没有额外查询，也没有额外往返：同样的扫描，在约 1700 列中
    -- 多读九列。
    COLUMNS('^ProfileEvent_(ReadBufferFromFileDescriptorReadBytes|WriteBufferFromFileDescriptorWriteBytes|DiskReadElapsedMicroseconds|DiskWriteElapsedMicroseconds|OSRead(Bytes|Chars)|OSWrite(Bytes|Chars)|OSIOWaitMicroseconds)$') APPLY sum,
    -- 原始覆盖事实，不假设采样间隔：metric_log 的
    -- collect_interval_milliseconds 可配置（默认 1 秒），且没有
    -- system 表暴露它。读取方据此推导间隔——
    -- 连续一小时的 span / (samples - 1)——因此覆盖秒数是
    -- samples x interval，样本数少于其 span 所暗示的小时
    -- 表示有遗漏（宕机、启动：启动后的第一个样本
    -- 可能携带计数器的累计总量，测得单个样本中有
    -- 3.5e12 us 的 OSIOWait）。仅凭一个样本无法确定间隔。
    count()                                                  AS metric_samples,
    dateDiff('second', min(event_time), max(event_time))     AS span_s,
    -- 该小时的首/末样本，因此一个窗口的 span 是跨 bucket 的
    -- last − first，包含空隙（keeper_xid_renewal_due 检查）。
    toString(min(event_time))                                AS first_sample,
    toString(max(event_time))                                AS last_sample,
    COLUMNS('^CurrentMetric_Background(Fetches|Schedule|MergesAndMutations)PoolSize$') APPLY max,
    COLUMNS('^ProfileEvent_SharedMergeTree(DataPartsFetchAttempt|DataPartsFetchFromS3|DataPartsFetchFromPeer|SelectPartsForRendezvousFetchParts|SelectPartsForCoordinatedFetchParts|HandleFetchPartsMicroseconds|VirtualPartsUpdatesLeaderSuccessfulElection|VirtualPartsUpdatesLeaderFailedElection|ScheduleDataProcessingJob)$') APPLY sum
FROM clusterAllReplicas(default, merge(system, '^metric_log'))
WHERE event_time > {from:3d} AND event_time <= {to:now}
GROUP BY hostname, time
ORDER BY hostname, time
