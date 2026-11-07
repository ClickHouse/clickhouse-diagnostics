-- 过去一天每小时、每个操作和错误码的失败 Keeper 请求。
-- zookeeper_log 是繁忙集群上最大的 system 表（每个请求和每个响应
-- 各一行——每天数十 GiB），因此这个 collector 不会
-- 聚合整张表：PREWHERE 只保留 error 不为 ZOK 的 Response 行，
-- 对每一行只读取两个 1 字节 Enum 列，对失败行才读取其余列
-- 数据。每小时的量和延迟改为来自 metric_log
-- (ZooKeeperTransactions, ZooKeeperWaitMicroseconds / ZooKeeperTransactions)；
-- 会话频繁变化来自 text_log_keeper_1_day。仅当配置了
-- <zookeeper_log> 时该表才存在。
-- Cloud 根文件（< 24.3）：按副本，duration_ms。24.3.1.0 变体读取
-- 微秒列；两个版本的输出名称仍保持毫秒。
SELECT
    toStartOfHour(event_time)            AS time,
    hostName()                           AS hostname,
    op_num,
    error,
    count()                              AS failed_requests,
    uniq(session_id)                     AS sessions_affected,
    max(duration_ms)                     AS max_duration_ms
FROM clusterAllReplicas(default, system.zookeeper_log)
PREWHERE type = 'Response' AND error IS NOT NULL AND error != 'ZOK'
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY time, hostname, op_num, error
ORDER BY time, failed_requests DESC
LIMIT 20000
