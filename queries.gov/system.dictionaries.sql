SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(name, '%salt%'))) AS name,
  status,
  -- origin 是 XML 配置路径（或带限定的 DDL 标识符）——
  -- 两者都会泄露基础设施/schema 细节，所以像名称一样哈希。
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
