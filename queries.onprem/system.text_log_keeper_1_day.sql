-- Keeper session markers per hour, all levels, over the last day: when did the
-- session expire, when was it finalized, when did the server reconnect and to
-- which Keeper host (the example keeps the "Connected to ZooKeeper at host:port"
-- line). These lines are Information/Debug level, so the Warning-and-worse
-- text_log slice never contains them. Pairs with metric_log ZooKeeper counters
-- (health-checks HC-3.8, the Keeper health test).
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
    anyLast(leftUTF8(message, 200))    AS example
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
