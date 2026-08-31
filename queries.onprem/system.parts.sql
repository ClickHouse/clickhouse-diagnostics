SELECT  
  partition,
  name,
  part_type,
  active,
  marks,
  rows,
  bytes_on_disk,
  data_compressed_bytes,
  data_uncompressed_bytes,
  secondary_indices_compressed_bytes,
  secondary_indices_uncompressed_bytes,
  secondary_indices_marks_bytes,
  marks_bytes,
  modification_time,
  remove_time,
  refcount,
  min_date,
  max_date,
  min_time,
  max_time,
  partition_id,
  min_block_number,
  max_block_number,
  level,
  data_version,
  primary_key_bytes_in_memory,
  primary_key_bytes_in_memory_allocated,
  is_frozen,
  database,
  table,
  engine,
  disk_name,
  path,
  hash_of_all_files,
  hash_of_uncompressed_files,
  uncompressed_hash_of_compressed_files,
  delete_ttl_info_min,
  delete_ttl_info_max,
  `move_ttl_info.expression`,
  `move_ttl_info.min`,
  `move_ttl_info.max`
FROM system.parts
-- Bounded so the collector cannot dominate the run on the cluster that
-- most needs it: a TOO_MANY_PARTS box is exactly where system.parts has
-- exploded, and the result is fully buffered in memory before it is
-- written. Inactive parts are dropped, the largest parts come first, and
-- the row cap keeps the worst case to tens of MB rather than GB.
WHERE active = 1
ORDER BY bytes_on_disk DESC
LIMIT 50000
