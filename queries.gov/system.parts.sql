SELECT
  hex(SHA256(concat(name, '%salt%'))) AS name,
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  -- The human-readable partition VALUE is customer data (often a tenant
  -- id or business key), so it is hashed. partition_id below is kept raw:
  -- it is the derived identifier support needs to correlate parts, and
  -- carries no free-form customer string.
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
  -- TTL expressions embed column names, which gov hashes in
  -- system.columns — hash the expression text for consistency. The
  -- min/max timestamps carry no identifiers and stay raw.
  arrayMap(e -> hex(SHA256(concat(e, '%salt%'))), `move_ttl_info.expression`) AS `move_ttl_info.expression`,
  `move_ttl_info.min`,
  `move_ttl_info.max`
FROM system.parts
FORMAT Native
