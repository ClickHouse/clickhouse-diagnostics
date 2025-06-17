SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  elapsed,
  progress,
  num_parts,
  result_part_name,
  is_mutation,
  total_size_bytes_compressed,
  total_size_marks,
  bytes_read_uncompressed,
  rows_read,
  bytes_written_uncompressed,
  rows_written,
  memory_usage,
  thread_id,
  merge_type,
  merge_algorithm
FROM system.merges
FORMAT Native
