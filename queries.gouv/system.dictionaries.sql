SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(name, '%salt%'))) AS name,
  status,
  origin,
  uuid,
  type,
  bytes_allocated,
  query_count,
  hit_rate,
  found_rate,
  element_count,
  load_factor,
  lifetime_min,
  lifetime_max,
  loading_start_time,
  last_successful_update_time,
  loading_duration
FROM system.dictionaries
FORMAT Native
