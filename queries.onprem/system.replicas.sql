-- tier: essential
SELECT
    database,
    table,
    is_leader,
    can_become_leader,
    is_readonly,
    is_session_expired,
    -- log_max_index、log_pointer、total_replicas、active_replicas 和
    -- zookeeper_exception 不在这里：只要请求其中任何一个，服务器就会
    -- 从 Keeper 读取每张表的状态（每张 replicated 表一次往返——
    -- 在拥有数万张表的服务器上要花几分钟；
    -- StorageSystemReplicas 的 with_zk_fields）。它们放在
    -- system.replicas_problem_tables 中，供需要它们的表使用。
    future_parts,
    parts_to_check,
    queue_size,
    inserts_in_queue,
    merges_in_queue,
    part_mutations_in_queue,
    toString(queue_oldest_time) AS queue_oldest_time,
    absolute_delay,
    -- last_queue_update_exception 是本地队列更新失败（开销低，
    -- 在内存中）；说明 replica 为何变为只读的 Keeper 侧
    -- zookeeper_exception 在 system.replicas_problem_tables 中。
    last_queue_update_exception
FROM system.replicas
ORDER BY absolute_delay DESC, database, table
