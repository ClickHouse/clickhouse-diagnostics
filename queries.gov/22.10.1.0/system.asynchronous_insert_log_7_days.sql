-- 位于 22.10.1.0/，没有根目录对应文件：asynchronous_insert_log 表本身
-- 是在 22.10 加入的，所以更旧的服务器会直接跳过此文件，而不是因为表
-- 不存在而报错。
--
-- 这个 22.10–23.3 变体只上报字节数：`rows` 列是在 23.4 加入的 —— 见
-- 23.4.1.0/system.asynchronous_insert_log_7_days.sql 中同时上报
-- total_rows 的变体。
SELECT
    toStartOfHour(event_time)                               AS time,
    hex(SHA256(concat(database, '%salt%')))                 AS database,
    hex(SHA256(concat(table, '%salt%')))                    AS table,
    status,
    count()                                                 AS flushes,
    sum(bytes)                                              AS total_bytes,
    -- 延迟（毫秒）：由 DateTime64(6) 的 *_microseconds 列通过浮点减法
    -- 计算（避免使用 dateDiff('millisecond',…)，22.10–22.12 不支持）。
    round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)  AS p90_flush_ms
FROM system.asynchronous_insert_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
-- 显式列出键，而不用 GROUP BY ALL（需要 22.12+）。
GROUP BY time, database, table, status
ORDER BY time, database, table
