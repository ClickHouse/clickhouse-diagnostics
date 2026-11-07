-- tier: essential
SELECT 
  create_time,
  -- database：没有它，当两个数据库存在同名表时，队列条目无法
  -- 与对应的 system.replicas / system.parts 行关联。
  -- replication_queue_errors 告警规则已经选择了它。
  database,
  table,
  type,
  replica_name,
  is_currently_executing,
  position,
  -- num_tries 区分“只排过一次、仍在等待”与“重试了数百次”——
  -- 这是慢队列与卡死队列的差别，且无法从 last_exception
  -- 推导出来。replication_queue_errors 告警规则
  -- 已经在消息中报告了它。
  num_tries,
  postpone_reason,
  last_exception,
  merge_type
FROM system.replication_queue
