SELECT 
  create_time,
  database,
  table,
  type,
  replica_name,
  is_currently_executing,
  position,
  num_tries,
  postpone_reason,
  last_exception,
  merge_type
FROM clusterAllReplicas(default, system.replication_queue)
