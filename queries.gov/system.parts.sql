SELECT
  hex(SHA256(concat(name, '%salt%'))) AS name,
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  -- 人类可读的 partition 值是客户数据（通常是租户 id 或业务键），
  -- 所以对它做哈希。下面的 partition_id 保持原样：
  -- 它是支持人员关联 part 所需的派生标识符，
  -- 不携带自由格式的客户字符串。
  hex(SHA256(concat(partition, '%salt%'))) AS partition,
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
  engine,
  delete_ttl_info_min,
  delete_ttl_info_max,
  -- TTL 表达式嵌入列名，而 gov 在 system.columns 中会哈希列名 ——
  -- 为保持一致，这里也哈希表达式文本。
  -- min/max 时间戳不携带标识符，保持原样。
  arrayMap(e -> hex(SHA256(concat(e, '%salt%'))), `move_ttl_info.expression`) AS `move_ttl_info.expression`,
  `move_ttl_info.min`,
  `move_ttl_info.max`
FROM system.parts
-- 限制行数，避免采集器在最需要它的集群上反而主导整次运行：
-- TOO_MANY_PARTS 的机器正是 system.parts 膨胀最厉害的地方，
-- 而结果在写出前会完整缓冲在内存中。非活跃 part 会被丢弃，
-- 最大的 part 排在前面，行数上限把最坏情况限制在数十 MB
-- 而不是 GB。
WHERE active = 1
ORDER BY bytes_on_disk DESC
LIMIT 50000
