-- 23.5+ 变体：包含 database_shard_name、database_replica_name 和 name（23.5 加入 system.clusters）。
-- 有意不选择 is_active：请求它（或请求
-- replication_lag、recovery_time、unsynced_after_recovery、
-- is_shared_catalog_cluster）会让服务器向每个 Replicated 数据库
-- 副本询问状态，任何过滤都阻止不了 —— 在拥有数千个 Replicated 数据库
-- 的服务器上每次运行 45–205 秒。Replicated 数据库的副本健康信息在
-- system.database_replicas（25.9+）。22.8 基线见根目录文件。
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

    hex(SHA256(concat(name, '%salt%'))) AS name
FROM
    system.clusters
