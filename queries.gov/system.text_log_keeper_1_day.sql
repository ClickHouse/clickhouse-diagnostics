-- tier: essential
-- 最近一天每小时、所有级别的 Keeper 会话标记：会话何时过期、
-- 何时被 finalize、服务器何时重连以及连到哪台 Keeper 主机
-- （示例保留 "Connected to ZooKeeper at host:port" 这一行）。
-- 这些行是 Information/Debug 级别，所以 Warning 及更严重的
-- text_log 切片永远不会包含它们。与 metric_log 的 ZooKeeper 计数器
-- 配合（health-checks HC-3.8，Keeper 健康测试）。
-- Gov：标记类别是固定词汇（不含标识符）；示例行
-- 指名 Keeper 主机，会被哈希。
SELECT
    toStartOfHour(event_time)          AS time,
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
    hex(SHA256(concat(anyLast(leftUTF8(message, 200)), '%salt%'))) AS example
FROM system.text_log
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  AND (logger_name LIKE 'ZooKeeperClient%' OR logger_name LIKE '%ZooKeeper%' OR logger_name LIKE '%Keeper%'
       OR message LIKE '%Session expired%' OR message LIKE '%Finalizing session%'
       OR message LIKE '%Connected to ZooKeeper%' OR message LIKE '%Trying to establish a new connection%'
       OR message LIKE '%Coordination::Exception%')
GROUP BY time, marker
ORDER BY time, count DESC
LIMIT 5000
