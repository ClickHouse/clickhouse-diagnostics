SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(name, '%salt%'))) AS name,
  uuid,
  engine,
  is_temporary,
  metadata_modification_time,
  storage_policy,
  has_own_data
FROM system.tables
FORMAT Native
