-- tier: essential
-- needs: keeper
-- system.replicas 中由 Keeper 支撑的那些列，只针对需要它们的表。
-- 向 system.replicas 请求 log_max_index、log_pointer、
-- total_replicas、active_replicas 或 zookeeper_exception，会让服务器
-- 从 Keeper 读取每一张表的状态——每张 replicated 表一次往返，
-- 在拥有数万张表的服务器上要花几分钟（实测每次运行
-- 18–97 s，告警里还要再花一遍）。system.replicas 会在读取状态
-- 之前先按 database/table 过滤，所以这个文件只对按廉价列选出的
-- 至多 200 张表请求这些字段：先选所有只读、
-- 会话过期、有排队、有延迟或在检查 part 的表，然后
-- 再取一部分健康表作为样本——足以发现仍注册在 Keeper 里的陈旧
-- replica（total_replicas 多于实际存在的服务器，P-43），以及
-- 落后于日志的 replica（log_pointer 远远落后于 log_max_index）。
-- 每张表的廉价列在 system.replicas 中。
SELECT
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
FROM system.replicas
WHERE (database, table) IN
(
    SELECT database, table
    FROM system.replicas
    ORDER BY (is_readonly OR is_session_expired OR queue_size > 0 OR absolute_delay > 0
              OR future_parts > 0 OR parts_to_check > 0) DESC,
             absolute_delay DESC, database, table
    LIMIT 200
)
ORDER BY absolute_delay DESC, database, table
