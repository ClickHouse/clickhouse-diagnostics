-- tier: essential
-- 24.1+ variant: adds is_killed (system.mutations gained it in 24.1); the root file is the 22.8 baseline.
SELECT 
  database,
  table,
  mutation_id,
  command,
  create_time,
  parts_to_do_names,
  parts_to_do,
  -- 24.1+: a KILL MUTATION in progress is not a stuck mutation
  is_killed
FROM system.mutations
