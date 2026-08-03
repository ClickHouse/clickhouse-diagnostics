-- 23.5+ variant: includes database_shard_name, database_replica_name, is_active and name (added to system.clusters in 23.5). See the root file for the 22.8 baseline.
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
  is_active,
  name
FROM system.clusters
FORMAT Native
