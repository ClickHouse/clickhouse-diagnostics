-- tier: essential
SELECT 
  -- 每个副本都有自己的队列：主机信息用于区分它们（
  -- dashboard 显示服务副本的队列，与本地读取时一样）
  hostName() AS hostname,
  create_time,
  database,
  table,
  type,
  replica_name,
  is_currently_executing,
  position,
  num_tries,
  postpone_reason,
  last_exception,
  merge_type
FROM clusterAllReplicas(default, system.replication_queue)
