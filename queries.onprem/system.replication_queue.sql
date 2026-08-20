SELECT 
  create_time,
  table,
  type,
  replica_name,
  is_currently_executing,
  position,
  postpone_reason,
  last_exception,
  merge_type
FROM system.replication_queue
