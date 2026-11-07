-- tier: essential
SELECT
    toStartOfHour(event_time)           AS time,
    avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS avg_merge_pool_tasks,
    max(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS max_merge_pool_tasks,
    avg(CurrentMetric_BackgroundFetchesPoolTask)             AS avg_fetch_pool_tasks,
    -- 池饱和是 fetch 延迟升级时被关注的那项发现：每台副本上 fetch
    -- 池 16/16、schedule 池 512/512，只能从这张表手动读取。
    -- avg() 旁边放 max()，以便区分整小时都顶在容量上的池
    -- 和只碰到过一次的池。四个 CurrentMetric_* 在 22.8 都存在；
    -- 容量值来自 background_*_pool_size
    -- （23.3 之前在 system.settings，23.3 起在 system.server_settings）。
    max(CurrentMetric_BackgroundFetchesPoolTask)             AS max_fetch_pool_tasks,
    avg(CurrentMetric_BackgroundSchedulePoolTask)            AS avg_schedule_pool_tasks,
    max(CurrentMetric_BackgroundSchedulePoolTask)            AS max_schedule_pool_tasks,
    avg(CurrentMetric_BackgroundCommonPoolTask)              AS avg_common_pool_tasks,
    max(CurrentMetric_BackgroundCommonPoolTask)              AS max_common_pool_tasks,
    -- 与 cloud 文件相同的列集合，那里这些值取各副本小时平均的
    -- 最大值；单主机场景，所以它们等于 avg_*。
    avg(CurrentMetric_BackgroundFetchesPoolTask)             AS max_replica_avg_fetch_pool_tasks,
    avg(CurrentMetric_BackgroundSchedulePoolTask)            AS max_replica_avg_schedule_pool_tasks,
    avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask)  AS max_replica_avg_merge_pool_tasks,
    avg(CurrentMetric_InterserverConnection)                 AS avg_interserver_connections,
    sum(ProfileEvent_ZooKeeperTransactions)                  AS zk_transactions,
    sum(ProfileEvent_ZooKeeperHardwareExceptions)            AS zk_hw_exceptions,
    -- 本地磁盘 I/O，这样“磁盘是瓶颈吗？”至少能从
    -- bundle 中得到回答。有个支持案例仅凭客户偶然发来的截图，
    -- 就建议为缓慢的 merge 提高 EBS 吞吐，因为这里没有任何东西
    -- 携带该信息。每个计数器都按线程求和，所以这些是总量，
    -- 不是设备测量值：OSRead*/OSWrite* Bytes 是块设备实际提供的
    -- 字节（不含页缓存）—— 按覆盖的秒数算，就是该小时的平均流量；
    -- ReadBuffer*/WriteBuffer* 这一对是 ClickHouse 请求的量，
    -- 因此二者能显示缓存吸收了多少；
    -- Disk*ElapsedMicroseconds 是读写调用内部的线程时间，
    -- 包含缓存命中，且重叠调用会累加，所以永远不能作为吞吐的
    -- 分母；OSIOWait 是阻塞在存储上的线程时间，
    -- 按覆盖的秒数算就是平均等待线程数。OS 计数器覆盖所有
    -- 本地卷：它们说明服务器存储有多忙，而
    -- 不是哪一块卷。
    --
    -- 按正则选择而不是点名：metric_log 的列集合随版本变化，
    -- 一个不存在的名称就会让整个采集器以 code 47 失败 ——
    -- 这正是 system.asynchronous_metrics 在 22.8 基线上出问题的方式。
    -- 服务器缺少某个计数器时，COLUMNS() 不会产出任何东西。
    -- 在 226 663 行（7 天）上实测成本：采集器读取 6.1 -> 13.0
    -- MiB，文件从 20.8 -> 28.8 KiB，墙钟时间没有可测变化。
    -- 没有额外查询，也没有额外往返：同一次扫描，
    -- 多出约 1700 列中的九列。
    COLUMNS('^ProfileEvent_(ReadBufferFromFileDescriptorReadBytes|WriteBufferFromFileDescriptorWriteBytes|DiskReadElapsedMicroseconds|DiskWriteElapsedMicroseconds|OSRead(Bytes|Chars)|OSWrite(Bytes|Chars)|OSIOWaitMicroseconds)$') APPLY sum,
    -- 原始覆盖事实，不假设采样间隔：metric_log 的
    -- collect_interval_milliseconds 可配置（默认 1 秒），且没有
    -- 系统表会暴露它。读取方从这些值推导间隔 ——
    -- 一个不间断小时的跨度 / (samples - 1) —— 所以覆盖秒数是
    -- samples x interval；如果某小时的样本数少于其跨度所示，
    -- 说明漏掉了一些（停机、启动：启动后的第一个样本
    -- 可能携带计数器累计总量，实测一个样本中出现
    -- 3.5e12 us 的 OSIOWait）。仅凭一个样本无法确定间隔。
    count()                                                  AS metric_samples,
    dateDiff('second', min(event_time), max(event_time))     AS span_s,
    -- 该小时的首/末样本，所以窗口跨度是跨桶的 last − first，
    -- 包含空隙（keeper_xid_renewal_due 检查）。
    toString(min(event_time))                                AS first_sample,
    toString(max(event_time))                                AS last_sample,
    avg(CurrentMetric_MemoryTracking)                        AS avg_memory_tracking_bytes
FROM system.metric_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time
ORDER BY time
