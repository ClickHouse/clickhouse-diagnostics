-- tier: essential
-- needs: keeper
-- system.replicas 中依赖 Keeper 的列，仅针对需要它们的表。
-- 向 system.replicas 请求 log_max_index、log_pointer、
-- total_replicas、active_replicas 或 zookeeper_exception 会让服务器
-- 从 Keeper 读取每一张表的状态 —— 每张复制表一次往返，
-- 在拥有数万张表的服务器上要几分钟（实测每次运行
-- 18–97 秒，告警中还要再花同样时间）。system.replicas 会在读取
-- 状态之前按 database/table 过滤，所以本文件只对由廉价列选出的
-- 最多 200 张表请求它们：先选所有只读、会话过期、有队列、有延迟
-- 或正在检查 part 的表，再取一部分健康表 —— 足以发现仍注册在
-- Keeper 中的陈旧副本（total_replicas 高于实际存在的服务器数，
-- P-43）以及落后于日志的副本（log_pointer 远落后于
-- log_max_index）。
-- 每张表的廉价列在 system.replicas 中。
-- Gov：名称和 Keeper 错误在外层查询中哈希，所以下面的
-- 过滤比较的是真实名称（否则名为 database 的别名会在 WHERE
-- 中遮蔽该列）。
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
    is_readonly,
    is_session_expired,
    queue_size,
    absolute_delay,
    log_max_index,
    log_pointer,
    total_replicas,
    active_replicas,
    -- Keeper 错误文本指名路径和表，所以会哈希；空值保持
    -- 为空，否则每个健康行都会携带同一个盐的哈希。
    if(zookeeper_exception = '', '', hex(SHA256(concat(zookeeper_exception, '%salt%')))) AS zookeeper_exception
FROM
(
    SELECT
        database, table, is_readonly, is_session_expired, queue_size, absolute_delay,
        log_max_index, log_pointer, total_replicas, active_replicas, zookeeper_exception
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
)
ORDER BY absolute_delay DESC, database, table
