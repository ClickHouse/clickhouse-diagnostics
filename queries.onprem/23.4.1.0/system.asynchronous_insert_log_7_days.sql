-- 23.4+ 变体：增加 total_rows。`rows` 列是在 23.4 加入
-- system.asynchronous_insert_log 的（22.10–23.3 没有该列，那些版本
-- 使用 22.10.1.0/ 中仅报告字节数的变体）。
SELECT
    toStartOfHour(event_time)           AS time,
    database,
    table,
    status,
    count()                             AS flushes,
    sum(rows)                           AS total_rows,
    sum(bytes)                          AS total_bytes,
    -- 延迟（毫秒）由 DateTime64(6) 的 *_microseconds 列通过
    -- 浮点减法得到——与 22.10.1.0/ 那一级完全一致，这样两个文件
    -- 以相同方式计算延迟（那一级需要这样来避开
    -- dateDiff('millisecond',…)，该写法在 22.10–22.12 不受支持）。
    round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)  AS p90_flush_ms
FROM system.asynchronous_insert_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time, database, table, status
ORDER BY time, database, table
