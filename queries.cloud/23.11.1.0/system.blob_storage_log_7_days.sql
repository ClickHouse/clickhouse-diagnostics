-- 每小时、每个副本的对象存储操作，包含失败和一条示例
-- 错误。仅当配置了 <blob_storage_log> 时该表才存在。
SELECT
    toStartOfHour(event_time)                                        AS time,
    hostname,
    event_type,
    disk_name,
    error != ''                                                      AS failed,
    count()                                                          AS count,
    sum(if(data_size = 18446744073709551615, 0, data_size))          AS bytes,
    anyLast(leftUTF8(error, 300))                                    AS example_error
FROM clusterAllReplicas(default, system.blob_storage_log)
WHERE event_date >= toDate({from:7d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time, hostname, event_type, disk_name, failed
ORDER BY time, count DESC
LIMIT 20000
