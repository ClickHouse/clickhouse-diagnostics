-- 24.2+ 变体：增加 system.tables.metadata_version，该列在
-- 24.2 加入（PR ClickHouse#59942，2024-02-16 合并——23.x/24.1 上没有）。
-- 此前这个文件被错误地限制在 23.8.1.0/ 下，导致工具在 23.8–24.1
-- 服务器上以 "Missing columns: 'metadata_version'" 失败。
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
  loading_dependent_table
FROM system.tables
