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
  -- dashboard 的 dictionaries 面板：键和属性布局，以及
  -- 服务器有 error_count 时的该列（COLUMNS() 在不具备该列的
  -- 版本上返回空，而不会报错）。
  key.names,
  key.types,
  attribute.names,
  attribute.types,
  COLUMNS('^error_count$')
FROM clusterAllReplicas(default, system.dictionaries)
