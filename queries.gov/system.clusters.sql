-- Root variant for the oldest supported servers (22.8+):
-- database_shard_name, database_replica_name, is_active and name were
-- added to system.clusters in 23.5 — see 23.5.1.0/system.clusters.sql
-- for the variant that includes them.
SELECT
    hex(SHA256(concat(cluster, '%salt%'))) AS cluster,
    shard_num,
    shard_weight,
    replica_num,
    hex(SHA256(concat(host_name, '%salt%'))) AS host_name,
    hex(SHA256(concat(host_address, '%salt%'))) AS host_address,
    hex(SHA256(concat(toString(port), '%salt%'))) AS port,
    is_local,
    hex(SHA256(concat(user, '%salt%'))) AS user,
    hex(SHA256(concat(default_database, '%salt%'))) AS default_database,
    errors_count,
    slowdowns_count,
    estimated_recovery_time
FROM
    system.clusters
FORMAT Native
