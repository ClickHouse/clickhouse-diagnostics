SELECT
  create_time,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  type,
  -- replica_name is conventionally the {replica} macro, i.e. a hostname —
  -- hashed like host_name in system.clusters.
  hex(SHA256(concat(replica_name, '%salt%'))) AS replica_name,
  is_currently_executing,
  position,
  -- postpone_reason and last_exception are server-generated text that
  -- routinely names parts, tables and paths, so both are hashed (as
  -- system.errors does with last_error_message).
  --
  -- The if(… = '', '', …) guard matters: hashing an empty string yields
  -- hex(SHA256(salt)) — a constant 64-char hex on EVERY healthy row —
  -- which would destroy the "is this entry erroring at all?" signal and
  -- trip the dashboard's `length > 1` error-row heuristic for all rows.
  if(postpone_reason = '', '', hex(SHA256(concat(postpone_reason, '%salt%')))) AS postpone_reason,
  if(last_exception = '', '', hex(SHA256(concat(last_exception, '%salt%')))) AS last_exception,
  merge_type
FROM system.replication_queue
