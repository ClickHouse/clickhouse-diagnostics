SELECT 
  hex(SHA256(concat(user, '%salt%'))) AS user,
  hex(SHA256(concat(address, '%salt%'))) AS address,
  elapsed,
  read_rows,
  read_bytes,
  total_rows_approx,
  memory_usage,
  query_id,
  is_cancelled,
  is_all_data_sent
FROM system.processes
FORMAT Native
