SELECT 
  hostName() AS hostname,
  database,
  name,
  uuid,
  status,
  origin,
  type,
  bytes_allocated,
  query_count,
  hit_rate,
  found_rate,
  element_count,
  load_factor,
  source,
  lifetime_min,
  lifetime_max,
  loading_start_time,
  last_successful_update_time,
  --error_count,
  loading_duration,
  last_exception,
  comment,
  -- The dashboard's dictionaries panel: the key and attribute layout, and
  -- error_count where the server has it (COLUMNS() yields nothing on a
  -- version without it, never an error).
  key.names,
  key.types,
  attribute.names,
  attribute.types,
  COLUMNS('^error_count$')
FROM clusterAllReplicas(default, system.dictionaries)
