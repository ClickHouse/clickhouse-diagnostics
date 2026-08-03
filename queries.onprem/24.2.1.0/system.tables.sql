-- 24.2+ variant: adds system.tables.metadata_version, which was added in
-- 24.2 (PR ClickHouse#59942, merged 2024-02-16 — absent on 23.x/24.1).
-- Previously this file was mis-gated under 23.8.1.0/, so the tool failed
-- on 23.8–24.1 servers with "Missing columns: 'metadata_version'".
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
  has_own_data,
  loading_dependencies_database,
  loading_dependencies_table,
  loading_dependent_database,
  loading_dependent_table
FROM system.tables
FORMAT Native
