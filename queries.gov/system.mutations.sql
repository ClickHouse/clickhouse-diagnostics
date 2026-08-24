SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  mutation_id,
  hex(SHA256(concat(command, '%salt%'))) AS command,
  create_time,
  parts_to_do_names,
  parts_to_do
FROM system.mutations
