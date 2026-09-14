-- Object-storage operations per hour (Upload / Delete / MultiPartUpload* …) and
-- how many failed, with one example error. Answers "did the server delete or
-- fail to write the blob that a query later could not find" without
-- system.remote_data_paths (too large to ship). Table exists only when
-- <blob_storage_log> is configured (23.11+); absent = not enabled, not healthy.
-- Delete rows carry data_size = UInt64 max — excluded from the byte sum.
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
