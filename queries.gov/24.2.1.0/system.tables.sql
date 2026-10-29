-- 24.2+ variant: adds system.tables.metadata_version, which was added in
-- 24.2 (PR ClickHouse#59942, merged 2024-02-16 — absent on 23.x/24.1).
-- Previously this file was mis-gated under 23.8.1.0/, so the tool failed
-- on 23.8–24.1 servers with "Missing columns: 'metadata_version'".
--
-- This is deliberately gov's TOP rung: gov does not collect
-- parameterized_view_parameters (the 25.4 addition in onprem/cloud) —
-- parameter names are identifier-like and gov hashes identifiers.
SELECT
  hex(SHA256(concat(database, '%salt%'))) AS database,
  -- not an identifier: lets the disk_space_low check leave out the disks
  -- only system tables use (qualified: the bare name is the hashed alias)
  toUInt8(system.tables.database IN ('system', 'information_schema', 'INFORMATION_SCHEMA')) AS is_system_db,
  hex(SHA256(concat(name, '%salt%'))) AS name,
  uuid,
  engine,
  is_temporary,
  metadata_modification_time,
  metadata_version,
  -- hashed with the same salt as system.storage_policies.policy_name so the
  -- two files join; empty (no policy / non-MergeTree) stays empty.
  if(storage_policy = '', '', hex(SHA256(concat(storage_policy, '%salt%')))) AS storage_policy,
  has_own_data,
  -- Numbers, not identifiers: safe to ship raw.
  total_rows,
  total_bytes,
  -- The dependency edges of the schema graph, hashed element by element with
  -- the same salt as `database` / `name` above, so an edge still joins to the
  -- hashed table it points at and the mapping CSV reverses both ends. An
  -- empty array hashes to an empty array.
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), dependencies_database)         AS dependencies_database,
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), dependencies_table)            AS dependencies_table,
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), loading_dependencies_database) AS loading_dependencies_database,
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), loading_dependencies_table)    AS loading_dependencies_table
FROM system.tables
