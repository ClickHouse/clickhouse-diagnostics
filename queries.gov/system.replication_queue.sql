SELECT
  create_time,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  type,
  replica_name,
  is_currently_executing,
  position,
  postpone_reason,
  -- Exception text can embed database/table names — hash it like
  -- system.errors does with last_error_message.
  hex(SHA256(concat(last_exception, '%salt%'))) AS last_exception,
  merge_type
FROM system.replication_queue
FORMAT Native
