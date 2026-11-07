-- tier: essential
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
    is_leader,
    can_become_leader,
    is_readonly,
    is_session_expired,
    -- log_max_index、log_pointer、total_replicas、active_replicas 和
    -- zookeeper_exception 不在这里：请求其中任何一个都会让服务器
    -- 从 Keeper 读取每张表的状态（每张复制表一次往返 ——
    -- 在拥有数万张表的服务器上要几分钟；
    -- StorageSystemReplicas 的 with_zk_fields）。它们位于
    -- system.replicas_problem_tables，供需要它们的表使用。
    future_parts,
    parts_to_check,
    queue_size,
    inserts_in_queue,
    merges_in_queue,
    part_mutations_in_queue,
    toString(queue_oldest_time)             AS queue_oldest_time,
    absolute_delay,
    -- 服务器生成的文本，经常指名路径、表和 Keeper znode，
    -- 所以像 system.replication_queue 中的 postpone_reason/last_exception
    -- 一样哈希。if(… = '', '', …) 守卫很重要：哈希空字符串
    -- 会得到 hex(SHA256(salt)) —— 每个健康行上的常量 ——
    -- 从而破坏“该副本到底有没有报错？”这一信号。
    if(last_queue_update_exception = '', '', hex(SHA256(concat(last_queue_update_exception, '%salt%')))) AS last_queue_update_exception
FROM system.replicas
ORDER BY absolute_delay DESC, database, table
