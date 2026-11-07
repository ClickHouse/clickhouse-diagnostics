-- tier: essential
-- 24.1+ 变体：增加 is_killed（system.mutations 在 24.1 获得该列）；根文件是 22.8 基线。
SELECT 
  database,
  table,
  mutation_id,
  command,
  create_time,
  parts_to_do_names,
  parts_to_do,
  -- 24.1+：正在进行的 KILL MUTATION 不算卡住的 mutation
  is_killed
FROM system.mutations
