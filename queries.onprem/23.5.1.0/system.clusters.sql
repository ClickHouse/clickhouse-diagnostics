-- 23.5+ 变体：包含 database_shard_name、database_replica_name 和 name（23.5 加入 system.clusters）。
-- is_active 被刻意不选择：请求它（或请求
-- replication_lag、recovery_time、unsynced_after_recovery、
-- is_shared_catalog_cluster）会让服务器向每个 Replicated 数据库的
-- replica 询问状态，任何过滤都无法阻止——在拥有数千个 Replicated 数据库
-- 的服务器上每次运行要 45–205 s。Replicated 数据库的 replica 健康状况
-- 见 system.database_replicas（25.9+）。22.8 基线见根文件。
SELECT 
  cluster,
  shard_num,
  shard_weight,
  replica_num,
  host_name,
  host_address,
  port,
  is_local,
  user,
  default_database,
  errors_count,
  slowdowns_count,
  estimated_recovery_time,
  database_shard_name,
  database_replica_name,

  name
FROM system.clusters
