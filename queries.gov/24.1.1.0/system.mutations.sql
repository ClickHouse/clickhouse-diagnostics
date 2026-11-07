-- tier: essential
-- 24.1+ 变体：增加 is_killed（system.mutations 在 24.1 获得它）；根目录文件是 22.8 基线。
SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  mutation_id,
  hex(SHA256(concat(command, '%salt%'))) AS command,
  create_time,
  parts_to_do_names,
  parts_to_do,
  -- 24.1+：正在进行的 KILL MUTATION 不是卡住的 mutation
  is_killed
FROM system.mutations
