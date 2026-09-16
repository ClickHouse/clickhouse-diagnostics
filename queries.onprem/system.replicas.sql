SELECT
    database,
    table,
    is_leader,
    can_become_leader,
    is_readonly,
    is_session_expired,
    future_parts,
    parts_to_check,
    queue_size,
    inserts_in_queue,
    merges_in_queue,
    part_mutations_in_queue,
    toString(queue_oldest_time) AS queue_oldest_time,
    log_max_index,
    log_pointer,
    absolute_delay,
    total_replicas,
    active_replicas,
    -- is_readonly = 1 says the replica stopped accepting writes; these two say
    -- why. zookeeper_exception carries the Keeper-side error and
    -- last_queue_update_exception the local queue-update failure, so a
    -- read-only replica can be told apart from a lost Keeper session without a
    -- second round trip to the customer. Both exist since 22.8 (the oldest
    -- server this query set supports) and zookeeper_exception adds no cost
    -- here: log_max_index, log_pointer, total_replicas and active_replicas
    -- already put this query in StorageSystemReplicas' with_zk_fields path.
    last_queue_update_exception,
    zookeeper_exception
FROM system.replicas
ORDER BY absolute_delay DESC, database, table
