SELECT 
  database,
  table,
  mutation_id,
  command,
  create_time,
  parts_to_do_names,
  parts_to_do
  --is_killed
FROM system.mutations
FORMAT Native
