-- tier: essential
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
    is_leader,
    can_become_leader,
    is_readonly,
    is_session_expired,
    -- log_max_index, log_pointer, total_replicas, active_replicas and
    -- zookeeper_exception are not here: asking for any of them makes the
    -- server read every table's status from Keeper (one round trip per
    -- replicated table — minutes on a server with tens of thousands of
    -- tables; StorageSystemReplicas' with_zk_fields). They are in
    -- system.replicas_problem_tables, for the tables that need them.
    future_parts,
    parts_to_check,
    queue_size,
    inserts_in_queue,
    merges_in_queue,
    part_mutations_in_queue,
    toString(queue_oldest_time)             AS queue_oldest_time,
    absolute_delay,
    -- Server-generated text that routinely names paths, tables and Keeper
    -- znodes, so it is hashed like postpone_reason/last_exception in
    -- system.replication_queue. The if(… = '', '', …) guard matters: hashing
    -- an empty string yields hex(SHA256(salt)) — a constant on every healthy
    -- row — which would destroy the "is this replica erroring at all?" signal.
    if(last_queue_update_exception = '', '', hex(SHA256(concat(last_queue_update_exception, '%salt%')))) AS last_queue_update_exception
FROM system.replicas
ORDER BY absolute_delay DESC, database, table
