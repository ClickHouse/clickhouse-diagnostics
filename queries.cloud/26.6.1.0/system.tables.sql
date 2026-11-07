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
  -- total_rows / total_bytes 是 schema graph 中的节点徽标，也是
  -- bundle 中回答“哪些表真正持有数据”的最廉价方式。两者都
  -- 早在 22.8 下限之前就存在。
  total_rows,
  total_bytes,
  has_own_data,
  loading_dependencies_database,
  loading_dependencies_table,
  loading_dependent_database,
  loading_dependent_table,
  -- target_database / target_table（26.6）：materialized view（物化视图）
  -- 写入的表——显式 TO 目标，或使用 ENGINE 声明的 MV 的隐式
  -- .inner_id.<uuid> 表。26.6 之前，图会从
  -- dependencies_* 推断这条边，但会漏掉隐式目标。
  target_database,
  target_table,
  parameterized_view_parameters
FROM system.tables
