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
    active_replicas
FROM system.replicas
ORDER BY absolute_delay DESC, database, table
