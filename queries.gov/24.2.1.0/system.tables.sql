-- 24.2+ variant: adds system.tables.metadata_version, which was added in
-- 24.2 (PR ClickHouse#59942, merged 2024-02-16 — absent on 23.x/24.1).
-- Previously this file was mis-gated under 23.8.1.0/, so the tool failed
-- on 23.8–24.1 servers with "Missing columns: 'metadata_version'".
SELECT
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(name, '%salt%'))) AS name,
  uuid,
  engine,
  is_temporary,
  metadata_modification_time,
  metadata_version,
  storage_policy,
  has_own_data
FROM system.tables
FORMAT Native
