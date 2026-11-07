SELECT 
  hex(SHA256(concat(database, '%salt%'))) AS database,
  -- 不是标识符：让 disk_space_low 检查能排除只有系统表使用的磁盘
  -- （限定写法：裸名称是上面那个哈希别名）
  toUInt8(system.tables.database IN ('system', 'information_schema', 'INFORMATION_SCHEMA')) AS is_system_db,
  hex(SHA256(concat(name, '%salt%'))) AS name,
  uuid,
  engine,
  is_temporary,
  metadata_modification_time,
  -- 用与 system.storage_policies.policy_name 相同的盐哈希，使两个
  -- 文件可以关联；空值（无策略 / 非 MergeTree）保持为空。
  if(storage_policy = '', '', hex(SHA256(concat(storage_policy, '%salt%')))) AS storage_policy,
  has_own_data,
  -- 数字，不是标识符：可以原样收集。
  total_rows,
  total_bytes,
  -- schema graph 的依赖边，逐元素用与上面 `database` / `name`
  -- 相同的盐哈希，因此一条边仍能关联到它指向的哈希表，
  -- 映射 CSV 可反转两端。空数组
  -- 哈希后仍为空数组。
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), dependencies_database)         AS dependencies_database,
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), dependencies_table)            AS dependencies_table,
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), loading_dependencies_database) AS loading_dependencies_database,
  arrayMap(x -> hex(SHA256(concat(x, '%salt%'))), loading_dependencies_table)    AS loading_dependencies_table
FROM system.tables
