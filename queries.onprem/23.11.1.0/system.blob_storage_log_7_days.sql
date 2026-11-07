-- 每小时的对象存储操作（Upload / Delete / MultiPartUpload* …）以及
-- 失败了多少次，附一条示例错误。回答“服务器是否删除了、或未能写入
-- 之后某条查询找不到的 blob”，而无需
-- system.remote_data_paths（太大，无法随包采集）。只有当配置了
-- <blob_storage_log>（23.11+）时该表才存在；缺席 = 未启用，而非健康。
-- Delete 行的 data_size = UInt64 最大值——已从字节求和中排除。
SELECT
    toStartOfHour(event_time)                                        AS time,
    event_type,
    disk_name,
    error != ''                                                      AS failed,
    count()                                                          AS count,
    sum(if(data_size = 18446744073709551615, 0, data_size))          AS bytes,
    anyLast(leftUTF8(error, 300))                                    AS example_error
FROM system.blob_storage_log
WHERE event_date >= toDate({from:7d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time, event_type, disk_name, failed
ORDER BY time, count DESC
LIMIT 20000
