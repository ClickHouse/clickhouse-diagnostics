-- tier: essential
-- 过去一天每小时、所有级别的 Keeper 会话标记：会话何时
-- 过期、何时结束、服务器何时重新连接以及连到
-- 哪个 Keeper 主机（示例保留 "Connected to ZooKeeper at host:port"
-- 这一行）。这些行是 Information/Debug 级别，因此 Warning 及更严重的
-- text_log 切片永远不会包含它们。与 metric_log 的 ZooKeeper 计数器
-- （健康检查 HC-3.8，即 Keeper 健康测试）配合使用。
SELECT
    toStartOfHour(event_time)          AS time,
    hostName()                         AS hostname,
    multiIf(message LIKE '%Session expired%',                      'session_expired',
            message LIKE '%Finalizing session%',                   'session_finalized',
            message LIKE '%Connected to ZooKeeper%',               'connected',
            message LIKE '%Trying to establish a new connection%', 'reconnecting',
            message LIKE '%Connection loss%',                      'connection_loss',
            message LIKE '%Operation timeout%',                    'operation_timeout',
            'other_keeper') AS marker,
    count()                            AS count,
    min(event_time)                    AS first_seen,
    max(event_time)                    AS last_seen,
    anyLast(leftUTF8(message, 200))    AS example
FROM clusterAllReplicas(default, system.text_log)
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  AND (logger_name LIKE 'ZooKeeperClient%' OR logger_name LIKE '%ZooKeeper%' OR logger_name LIKE '%Keeper%'
       OR message LIKE '%Session expired%' OR message LIKE '%Finalizing session%'
       OR message LIKE '%Connected to ZooKeeper%' OR message LIKE '%Trying to establish a new connection%'
       OR message LIKE '%Coordination::Exception%')
GROUP BY time, hostname, marker
ORDER BY time, count DESC
LIMIT 5000
