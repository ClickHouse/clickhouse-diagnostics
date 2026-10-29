-- Replicated-database replicas on this server (25.9+): is each one writable,
-- does it have a Keeper session, and what did Keeper last refuse. This is
-- the cheap replacement for system.clusters.is_active, which made the server
-- ask every Replicated-database replica for its status.
-- max_log_ptr, log_ptr and total_replicas are left out on purpose: asking
-- for any of them reads every database's status from Keeper. DDL lag is in
-- system.distributed_ddl_queue.
-- Version-only: the table arrived in 25.9; older servers skip this file.
SELECT
    hex(SHA256(concat(database, '%salt%')))     AS database,
    is_readonly,
    is_session_expired,
    hex(SHA256(concat(shard_name, '%salt%')))   AS shard_name,
    hex(SHA256(concat(replica_name, '%salt%'))) AS replica_name,
    if(zookeeper_exception = '', '', hex(SHA256(concat(zookeeper_exception, '%salt%')))) AS zookeeper_exception
FROM system.database_replicas
ORDER BY is_readonly DESC, is_session_expired DESC, database
