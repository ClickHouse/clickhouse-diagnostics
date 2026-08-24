-- 23.5+ variant: includes database_shard_name, database_replica_name, is_active and name (added to system.clusters in 23.5). See the root file for the 22.8 baseline.
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
    estimated_recovery_time,
    hex(SHA256(concat(database_shard_name, '%salt%'))) AS database_shard_name,
    hex(SHA256(concat(database_replica_name, '%salt%'))) AS database_replica_name,
    is_active,
    hex(SHA256(concat(name, '%salt%'))) AS name
FROM
    system.clusters
