-- 位于 22.10.1.0/ 且没有根文件对应版本：
-- asynchronous_insert_log 表本身是在 22.10 加入的，所以更旧的
-- 服务器会直接跳过这个文件，而不会因表不存在而报错。
--
-- 这个 22.10–23.3 变体只报告字节数：`rows` 列是在
-- 23.4 加入的——还会报告 total_rows 的变体见
-- 23.4.1.0/system.asynchronous_insert_log_7_days.sql。
SELECT
    toStartOfHour(event_time)           AS time,
    database,
    table,
    status,
    count()                             AS flushes,
    sum(bytes)                          AS total_bytes,
    -- 延迟（毫秒）由 DateTime64(6) 的 *_microseconds 列（自 22.10 起
    -- 存在）通过浮点减法得到。这里避免使用 dateDiff('millisecond',…)：
    -- 22.10–22.12 不支持该亚秒单位（BAD_ARGUMENTS）。
    round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)  AS p90_flush_ms
FROM system.asynchronous_insert_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
-- 用显式键列表而不是 GROUP BY ALL（后者需要 22.12+）；这个文件
-- 也必须在 22.10 和 22.11 上运行。
GROUP BY time, database, table, status
ORDER BY time, database, table
