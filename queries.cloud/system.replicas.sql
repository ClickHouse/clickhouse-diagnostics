-- tier: essential
SELECT
    -- 按副本。每个副本保留自己的 system.replicas，在
    -- SharedMergeTree 上队列列也是按副本的：inserts_in_queue
    -- 是该副本尚未 fetch 的 level-0 part 数量，
    -- absolute_delay 是其中最老一个的年龄，oldest_part_to_get /
    -- inserts_oldest_time 给出它的名称和日期。因此同一张表的两个副本
    -- 可能不一致，而主机信息用于区分它们。
    hostName() AS hostname,
    database,
    table,
    is_leader,
    can_become_leader,
    is_readonly,
    is_session_expired,
    -- log_max_index、log_pointer、total_replicas、active_replicas 和
    -- zookeeper_exception 不在这里：请求其中任何一个都会让
    -- 服务器从 Keeper 读取每张表的状态（每张复制表一次往返——
    -- 在拥有数万张表的服务器上要花数分钟；
    -- StorageSystemReplicas 的 with_zk_fields）。它们位于
    -- system.replicas_problem_tables，仅用于需要它们的表。
    future_parts,
    parts_to_check,
    queue_size,
    inserts_in_queue,
    merges_in_queue,
    part_mutations_in_queue,
    toString(queue_oldest_time) AS queue_oldest_time,
    absolute_delay,
    -- 该副本仍缺少的最老 part 及其写入时间：
    -- absolute_delay 衡量延迟大小，这两列说明具体是哪个 part，因此同一个
    -- part 在多台主机上显示为“最老缺失”就是一个 finding。
    oldest_part_to_get,
    toString(inserts_oldest_time) AS inserts_oldest_time,
    -- 本地队列更新失败；Keeper 侧的 zookeeper_exception
    -- 在 system.replicas_problem_tables 中（见上文说明）。
    last_queue_update_exception
FROM clusterAllReplicas(default, system.replicas)
-- 问题行在前，然后是有界的健康行样本。fan-out 是
-- 表数 × 副本数：在拥有数万张表和约 30 个副本的自托管
-- SharedMergeTree 集群上，一个文件就是约 750 000 行（约 300 MiB），
-- 其中约 3 % 携带信号。把只读、过期、
-- 延迟和排队的行排到顶部，并每台主机保留 200 行，可将
-- 文件限制在主机数 × 200 行（30 台主机约 2 MiB），同时每个问题行
-- 都会保留，直到某台主机的问题行超过 200 条——预检查会把
-- 恰好 200 行的主机读作“已截断”（表数量本身在
-- system.tables 中）。每个副本复制表少于 200 张的服务
-- 不受影响。alert 和 dashboard 中的 {sys.replicas} 直接读取
-- system.replicas，不受此限制。
ORDER BY is_readonly DESC, is_session_expired DESC, absolute_delay DESC, inserts_in_queue DESC, database, table
LIMIT 200 BY hostname
