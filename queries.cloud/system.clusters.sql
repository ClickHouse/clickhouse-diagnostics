-- 面向最旧受支持服务器（22.8+）的根变体：
-- database_shard_name、database_replica_name、is_active 和 name 在
-- 23.5 加入 system.clusters——包含这些列的变体见
-- 23.5.1.0/system.clusters.sql。
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
  estimated_recovery_time
FROM system.clusters
