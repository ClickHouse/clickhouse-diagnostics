-- 23.5+ variant: includes database_shard_name, database_replica_name and name (added to system.clusters in 23.5).
-- is_active is deliberately NOT selected: asking for it (or for
-- replication_lag, recovery_time, unsynced_after_recovery,
-- is_shared_catalog_cluster) makes the server ask every Replicated-database
-- replica for its status, with no filter able to stop it — 45–205 s per run
-- on a server with thousands of Replicated databases. Replica health of
-- Replicated databases is in system.database_replicas (25.9+). See the root file for the 22.8 baseline.
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
