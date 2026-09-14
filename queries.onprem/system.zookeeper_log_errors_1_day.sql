-- Failed Keeper requests per hour, operation and error over the last day.
-- zookeeper_log is the largest system table on a busy cluster (one row per
-- request AND per response — tens of GiB a day), so this collector does NOT
-- aggregate the whole table: PREWHERE keeps only Response rows whose error is
-- not ZOK, reading just two 1-byte Enum columns for every row and the rest for
-- the failures. Volume and latency per hour come from metric_log instead
-- (ZooKeeperTransactions, ZooKeeperWaitMicroseconds / ZooKeeperTransactions);
-- session churn from text_log_keeper_1_day. Table exists only when
-- <zookeeper_log> is configured.
-- Root variant: duration_ms (< 24.3).
SELECT
    toStartOfHour(event_time)   AS time,
    op_num,
    error,
    count()                     AS failed_requests,
    uniq(session_id)            AS sessions_affected,
    max(duration_ms)            AS max_duration_ms
FROM system.zookeeper_log
PREWHERE type = 'Response' AND error IS NOT NULL AND error != 'ZOK'
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY time, op_num, error
ORDER BY time, failed_requests DESC
LIMIT 20000
