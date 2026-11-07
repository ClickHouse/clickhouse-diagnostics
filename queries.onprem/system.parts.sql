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
-- 设置上限，以免在恰恰最需要这份采集的集群上，collector 反而主导
-- 整次运行：TOO_MANY_PARTS 的机器正是 system.parts 行数爆炸
-- 的地方，而且结果在写入前会完整缓冲在内存中。
-- 不活跃的 part 会被丢弃，最大的 part 排在最前，行数上限
-- 把最坏情况控制在几十 MB 而非 GB。
WHERE active = 1
ORDER BY bytes_on_disk DESC
LIMIT 50000
