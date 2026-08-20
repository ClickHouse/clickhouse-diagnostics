SELECT 
  database,
  table,
  partition_id,
  bytes_on_disk,
  disk,
  path,
  reason,
  min_block_number,
  max_block_number,
  level
FROM clusterAllReplicas(default, system.detached_parts)
