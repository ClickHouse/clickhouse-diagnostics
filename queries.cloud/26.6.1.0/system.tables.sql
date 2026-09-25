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
  -- total_rows / total_bytes are the node badges in the schema graph and the
  -- cheapest "which tables actually hold data" answer in a bundle. Both exist
  -- since long before the 22.8 floor.
  total_rows,
  total_bytes,
  has_own_data,
  loading_dependencies_database,
  loading_dependencies_table,
  loading_dependent_database,
  loading_dependent_table,
  -- target_database / target_table (26.6): the table a materialized view
  -- writes to — the explicit TO target, or the implicit .inner_id.<uuid> table
  -- of an MV declared with an ENGINE. Before 26.6 the graph infers this edge
  -- from dependencies_*, which misses the implicit target.
  target_database,
  target_table,
  parameterized_view_parameters
FROM system.tables
