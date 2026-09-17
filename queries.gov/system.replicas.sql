SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
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
    toString(queue_oldest_time)             AS queue_oldest_time,
    log_max_index,
    log_pointer,
    absolute_delay,
    total_replicas,
    active_replicas,
    -- Server-generated text that routinely names paths, tables and Keeper
    -- znodes, so both are hashed like postpone_reason/last_exception in
    -- system.replication_queue. The if(… = '', '', …) guard matters: hashing
    -- an empty string yields hex(SHA256(salt)) — a constant on every healthy
    -- row — which would destroy the "is this replica erroring at all?" signal.
    if(last_queue_update_exception = '', '', hex(SHA256(concat(last_queue_update_exception, '%salt%')))) AS last_queue_update_exception,
    if(zookeeper_exception = '', '', hex(SHA256(concat(zookeeper_exception, '%salt%'))))                 AS zookeeper_exception
FROM system.replicas
ORDER BY absolute_delay DESC, database, table
