SELECT 
  create_time,
  hex(SHA256(concat(table, '%salt%'))) AS table_1,
  table,
  type,
  replica_name,
  is_currently_executing,
  position,
  postpone_reason,
  last_exception,
  merge_type
FROM system.replication_queue
FORMAT Native
