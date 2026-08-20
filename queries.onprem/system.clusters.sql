-- Root variant for the oldest supported servers (22.8+):
-- database_shard_name, database_replica_name, is_active and name were
-- added to system.clusters in 23.5 — see 23.5.1.0/system.clusters.sql
-- for the variant that includes them.
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
