-- tier: essential
SELECT
    toStartOfHour(event_time)           AS time,
    avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS avg_merge_pool_tasks,
    max(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS max_merge_pool_tasks,
    avg(CurrentMetric_BackgroundFetchesPoolTask)             AS avg_fetch_pool_tasks,
    -- Pool 饱和是 fetch-lag 升级处理时依赖的发现：每台 replica 上都出现
    -- fetch pool 16/16、schedule pool 512/512，是从这张表手工读出来的。
    -- 在 avg() 旁边放 max()，是为了区分“整小时都顶在上限”的池
    -- 与“只碰过一次上限”的池。四个
    -- CurrentMetric_* 在 22.8 上都存在；池大小是 background_*_pool_size
    -- （23.3 之前用 system.settings，从 23.3 起用 system.server_settings）。
    max(CurrentMetric_BackgroundFetchesPoolTask)             AS max_fetch_pool_tasks,
    avg(CurrentMetric_BackgroundSchedulePoolTask)            AS avg_schedule_pool_tasks,
    max(CurrentMetric_BackgroundSchedulePoolTask)            AS max_schedule_pool_tasks,
    avg(CurrentMetric_BackgroundCommonPoolTask)              AS avg_common_pool_tasks,
    max(CurrentMetric_BackgroundCommonPoolTask)              AS max_common_pool_tasks,
    -- 与 cloud 文件相同的列集，在那边这些列是各 replica 每小时均值
    -- 跨 replica 的最大值；这里只有一台主机，所以它们等于 avg_*。
    avg(CurrentMetric_BackgroundFetchesPoolTask)             AS max_replica_avg_fetch_pool_tasks,
    avg(CurrentMetric_BackgroundSchedulePoolTask)            AS max_replica_avg_schedule_pool_tasks,
    avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask)  AS max_replica_avg_merge_pool_tasks,
    avg(CurrentMetric_InterserverConnection)                 AS avg_interserver_connections,
    sum(ProfileEvent_ZooKeeperTransactions)                  AS zk_transactions,
    sum(ProfileEvent_ZooKeeperHardwareExceptions)            AS zk_hw_exceptions,
    -- dashboard 的 Keeper 面板：用户异常、请求延迟的
    -- 分子以及会话区间。这些在 22.8 上都存在。
    sum(ProfileEvent_ZooKeeperUserExceptions)                AS zk_user_exceptions,
    sum(ProfileEvent_ZooKeeperWaitMicroseconds)              AS zk_wait_us,
    min(CurrentMetric_ZooKeeperSession)                      AS zk_sessions_min,
    max(CurrentMetric_ZooKeeperSession)                      AS zk_sessions_max,
    -- 本地磁盘 I/O，这样“磁盘是不是瓶颈？”至少能从
    -- bundle 里回答。曾有一个支持案例，工程师仅凭客户偶然发来的
    -- 一张截图就建议提高 EBS 吞吐量来缓解慢 merge，因为当时
    -- 这里没有携带相关数据。每个计数器都按线程求和，所以这些是
    -- 总量，而非设备测量值：OSRead*/OSWrite* Bytes 是
    -- 块设备实际服务的量（不含 page cache）——除以覆盖的秒数，
    -- 就是该小时的平均流量；ReadBuffer*/WriteBuffer* 这一对是
    -- ClickHouse 请求的量，两者对比可以看出缓存吸收了多少；
    -- Disk*ElapsedMicroseconds 是 read/write 调用内部的线程时间，
    -- 包含缓存命中，重叠调用还会累加，所以绝不能作为吞吐量的
    -- 分母；OSIOWait 是线程在存储上阻塞的时间，除以
    -- 覆盖的秒数就是平均等待线程数。这些 OS
    -- 计数器覆盖所有本地卷：它们说明服务器存储有多忙，
    -- 而不是哪个卷忙。
    --
    -- 用正则选择而不是逐名列名：metric_log 的列集会随版本
    -- 变动，只要有一个名字不存在，整个 collector 就会以 code
    -- 47 失败——system.asynchronous_metrics 在 22.8 下限上
    -- 正是这样坏掉的。对于本服务器没有的计数器，COLUMNS() 返回空。
    -- 在 226 663 行（7 天）上实测开销：collector 读取量 6.1 -> 13.0
    -- MiB，文件增长 20.8 -> 28.8 KiB，实际耗时没有可测量的变化。
    -- 没有额外查询，也没有额外往返：同一次扫描，只是约 1700 列中
    -- 多了九列。
    COLUMNS('^ProfileEvent_(ReadBufferFromFileDescriptorReadBytes|WriteBufferFromFileDescriptorWriteBytes|DiskReadElapsedMicroseconds|DiskWriteElapsedMicroseconds|OSRead(Bytes|Chars)|OSWrite(Bytes|Chars)|OSIOWaitMicroseconds)$') APPLY sum,
    -- 原始覆盖事实，不假设采样间隔：metric_log 的
    -- collect_interval_milliseconds 是可配置的（默认 1 s），且没有
    -- system 表会暴露它。读取端根据这些数据推导间隔——
    -- 一个未中断小时的时间跨度 / (samples - 1)——所以覆盖秒数是
    -- samples x interval；若某小时样本数少于其跨度所暗示的
    -- 数量，说明漏掉了一些（停机、启动：启动后的第一个样本
    -- 可能带着计数器累积的总量，实测单个样本中就有
    -- 3.5e12 us 的 OSIOWait）。仅凭一个样本无法确定间隔。
    count()                                                  AS metric_samples,
    dateDiff('second', min(event_time), max(event_time))     AS span_s,
    -- 该小时的首/末样本，这样跨 bucket 的窗口跨度就是 last − first，
    -- 包含空隙（keeper_xid_renewal_due 检查会用到）。
    toString(min(event_time))                                AS first_sample,
    toString(max(event_time))                                AS last_sample,
    avg(CurrentMetric_MemoryTracking)                        AS avg_memory_tracking_bytes
FROM system.metric_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time
ORDER BY time
