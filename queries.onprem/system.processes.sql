SELECT 
  user,
  address,
  elapsed,
  read_rows,
  read_bytes,
  total_rows_approx,
  memory_usage,
  query_id,
  is_cancelled,
  is_all_data_sent
FROM system.processes
