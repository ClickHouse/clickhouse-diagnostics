-- 每小时的对象存储操作（Upload / Delete / MultiPartUpload* …）以及失败
-- 数量，附带一个示例错误。无需 system.remote_data_paths（体积太大，
-- 不便收集）就能回答“服务器是否删除了某个 blob，或写入失败，导致之后
-- 的查询找不到它”。仅当配置了 <blob_storage_log>（23.11+）时该表存在；
-- 不存在 = 未启用，而不是健康。
-- Delete 行的 data_size = UInt64 最大值 —— 已从字节总和中排除。
SELECT
    toStartOfHour(event_time)                                        AS time,
    event_type,
    hex(SHA256(concat(disk_name, '%salt%')))                         AS disk_name,
    error != ''                                                      AS failed,
    count()                                                          AS count,
    sum(if(data_size = 18446744073709551615, 0, data_size))          AS bytes,
    hex(SHA256(concat(anyLast(leftUTF8(error, 300)), '%salt%')))    AS example_error
FROM system.blob_storage_log
WHERE event_date >= toDate({from:7d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time, event_type, disk_name, failed
ORDER BY time, count DESC
LIMIT 20000
