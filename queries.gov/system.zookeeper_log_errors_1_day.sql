-- 最近一天每小时、按操作和错误统计的失败 Keeper 请求。
-- zookeeper_log 是繁忙集群上最大的系统表（每个请求和每个响应各一行 ——
-- 每天数十 GiB），所以本采集器不会聚合整张表：PREWHERE 只保留
-- error 不是 ZOK 的 Response 行，每行只读取两个 1 字节的 Enum 列，
-- 其余列只对失败行读取。每小时的量和延迟来自 metric_log
-- （ZooKeeperTransactions、ZooKeeperWaitMicroseconds /
-- ZooKeeperTransactions）；会话抖动来自 text_log_keeper_1_day。
-- 仅当配置了
-- <zookeeper_log> 时该表才存在。
-- 根目录变体：duration_ms（< 24.3）。
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
