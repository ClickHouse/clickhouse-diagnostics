-- tier: essential
-- Snapshot of async Distributed insert directories on this node. One row is
-- one shard directory (local files, not a shared table). Snapshot only:
-- no time window and no Keeper read. These columns exist on the 22.8 floor.
-- last_exception_time arrived in 23.3 and is not selected.
SELECT
  database,
  table,
  data_path,
  is_blocked,
  error_count,
  data_files,
  data_compressed_bytes,
  broken_data_files,
  broken_data_compressed_bytes,
  last_exception
FROM system.distribution_queue
