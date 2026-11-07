-- 24.2+ 变体：增加 system.tables.metadata_version，该列在
-- 24.2 中加入（PR ClickHouse#59942，2024-02-16 合并——23.x/24.1 上没有）。
-- 此前本文件被错误地放在 23.8.1.0/ 门槛下，导致工具在
-- 23.8–24.1 服务器上因 "Missing columns: 'metadata_version'" 而失败。
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
  loading_dependent_table
FROM system.tables
