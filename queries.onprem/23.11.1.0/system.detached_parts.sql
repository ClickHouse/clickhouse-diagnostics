SELECT
  database,
  table,
  partition_id,
  bytes_on_disk,
  modification_time,
  disk,
  path,
  reason,
  min_block_number,
  max_block_number,
  level
FROM system.detached_parts
