-- tier: essential
-- needs: keeper
-- system.replicas 中由 Keeper 支撑的列，仅用于需要它们的表。
-- 向 system.replicas 请求 log_max_index、log_pointer、
-- total_replicas、active_replicas 或 zookeeper_exception 会让服务器
-- 从 Keeper 读取“每张”表的状态——每张复制表一次往返，
-- 在拥有数万张表的服务器上要花数分钟（每次运行观察到
-- 18–97 秒，告警中还要再来一遍）。system.replicas 会在读取状态前
-- 按 database/table 过滤，因此本文件只对最多
-- 200 张由廉价列选出的表请求这些列：先是所有只读、
-- 会话过期、排队、延迟或正在检查 part 的表，然后
-- 抽样一些健康表——足以看到一个仍注册在 Keeper 中的陈旧副本
-- （total_replicas 高于实际存在的服务器数，P-43）以及一个
-- 落后于日志的副本（log_pointer 远落后于 log_max_index）。
-- 每张表的廉价列在 system.replicas 中。
-- Cloud：按副本。IN 子查询没有 GLOBAL，因此每个副本从自己的
-- system.replicas 中选择自己的最多 200 张表。
SELECT
    hostName() AS hostname,
    database,
    table,
    is_readonly,
    is_session_expired,
    queue_size,
    absolute_delay,
    log_max_index,
    log_pointer,
    total_replicas,
    active_replicas,
    zookeeper_exception
FROM clusterAllReplicas(default, system.replicas)
WHERE (database, table) IN
(
    SELECT database, table
    FROM system.replicas
    ORDER BY (is_readonly OR is_session_expired OR queue_size > 0 OR absolute_delay > 0
              OR future_parts > 0 OR parts_to_check > 0) DESC,
             absolute_delay DESC, database, table
    LIMIT 200
)
ORDER BY hostname, absolute_delay DESC, database, table
