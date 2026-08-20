SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(name, '%salt%'))) AS name,
  status,
  -- origin is the XML config path (or the qualified DDL identifier) —
  -- both reveal infrastructure/schema detail, so hash it like the names.
  hex(SHA256(concat(origin, '%salt%'))) AS origin,
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
