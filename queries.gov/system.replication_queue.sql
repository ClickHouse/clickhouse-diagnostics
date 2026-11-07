-- tier: essential
SELECT
  create_time,
  -- database 与 system.replicas 中完全相同地哈希，所以队列条目在
  -- bundle 内仍可关联到它的副本行。
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  type,
  -- replica_name 通常是 {replica} 宏，即主机名 ——
  -- 像 system.clusters 中的 host_name 一样哈希。
  hex(SHA256(concat(replica_name, '%salt%'))) AS replica_name,
  is_currently_executing,
  position,
  -- num_tries 是普通计数器，不含标识符。
  num_tries,
  -- postpone_reason 和 last_exception 是服务器生成的文本，
  -- 经常指名 part、表和路径，所以两者都做哈希（正如
  -- system.errors 对 last_error_message 所做的那样）。
  --
  -- if(… = '', '', …) 守卫很重要：哈希空字符串会得到
  -- hex(SHA256(salt)) —— 每个健康行上恒定的 64 字符十六进制 ——
  -- 这会破坏“该条目到底有没有报错？”这一信号，
  -- 并让 dashboard 的 `length > 1` 错误行启发式对所有行触发。
  if(postpone_reason = '', '', hex(SHA256(concat(postpone_reason, '%salt%')))) AS postpone_reason,
  if(last_exception = '', '', hex(SHA256(concat(last_exception, '%salt%')))) AS last_exception,
  merge_type
FROM system.replication_queue
