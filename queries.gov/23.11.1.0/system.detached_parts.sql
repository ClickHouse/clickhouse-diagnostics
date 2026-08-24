SELECT
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  partition_id,
  bytes_on_disk,
  modification_time,
  reason,
  min_block_number,
  max_block_number,
  level
FROM system.detached_parts
