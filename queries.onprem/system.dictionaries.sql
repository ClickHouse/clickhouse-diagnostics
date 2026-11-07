SELECT 
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
  loading_duration,
  last_exception,
  comment,
  -- dashboard 的字典面板：键与属性的布局，以及
  -- 服务器有 error_count 时取它（在没有此列的版本上 COLUMNS() 返回空，
  -- 而不会报错）。
  key.names,
  key.types,
  attribute.names,
  attribute.types,
  COLUMNS('^error_count$')
FROM system.dictionaries
