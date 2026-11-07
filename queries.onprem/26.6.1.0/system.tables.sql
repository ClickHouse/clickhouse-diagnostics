SELECT 
  database,
  name,
  uuid,
  engine,
  is_temporary,
  data_paths,
  metadata_path,
  metadata_modification_time,
  metadata_version,
  dependencies_database,
  dependencies_table,
  create_table_query,
  engine_full,
  as_select,
  partition_key,
  sorting_key,
  primary_key,
  sampling_key,
  storage_policy,
  comment,
  -- total_rows / total_bytes 是 schema graph 中的节点徽标，也是 bundle 里
  -- 回答“哪些表真正有数据”最廉价的方式。这两个列早在
  -- 22.8 下限之前就已存在。
  total_rows,
  total_bytes,
  has_own_data,
  loading_dependencies_database,
  loading_dependencies_table,
  loading_dependent_database,
  loading_dependent_table,
  -- target_database / target_table（26.6）：物化视图写入的表——
  -- 显式的 TO 目标，或使用 ENGINE 声明的 MV 的隐式
  -- .inner_id.<uuid> 表。在 26.6 之前，schema graph 从
  -- dependencies_* 推断这条边，会漏掉隐式目标。
  target_database,
  target_table,
  parameterized_view_parameters
FROM system.tables
