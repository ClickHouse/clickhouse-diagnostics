SELECT 
  create_time,
  -- database: without it a queue entry cannot be joined to the matching
  -- system.replicas / system.parts row when two databases hold a table of the
  -- same name. The replication_queue_errors alert rule already selects it.
  database,
  table,
  type,
  replica_name,
  is_currently_executing,
  position,
  -- num_tries separates "queued once, still waiting" from "retried hundreds of
  -- times" — the difference between a slow queue and a stuck one, and not
  -- derivable from last_exception. The replication_queue_errors alert rule
  -- already reports it in its message.
  num_tries,
  postpone_reason,
  last_exception,
  merge_type
FROM system.replication_queue
