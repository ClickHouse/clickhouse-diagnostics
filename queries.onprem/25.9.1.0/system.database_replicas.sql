-- Replicated-database replicas on this server (25.9+): is each one writable,
-- and does it have a Keeper session. This is
-- the cheap replacement for system.clusters.is_active, which made the server
-- ask every Replicated-database replica for its status.
-- max_log_ptr, log_ptr, total_replicas and zookeeper_exception are left out
-- on purpose: asking for any of them reads every database's status from
-- Keeper (zookeeper_exception joined that list upstream in October 2026,
-- ClickHouse/ClickHouse a6a695d). DDL lag is in
-- system.distributed_ddl_queue.
-- Version-only: the table arrived in 25.9; older servers skip this file.
SELECT
    database,
    is_readonly,
    is_session_expired,
    shard_name,
    replica_name
FROM system.database_replicas
ORDER BY is_readonly DESC, is_session_expired DESC, database
