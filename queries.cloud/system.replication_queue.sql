-- tier: essential
SELECT 
  -- every replica has its own queue: the host tells them apart (the
  -- dashboard shows the serving replica's, as a local read did)
  hostName() AS hostname,
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
