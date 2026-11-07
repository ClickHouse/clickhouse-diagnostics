-- 23.4+ 变体：增加 total_rows。`rows` 列是在 23.4 加入
-- system.asynchronous_insert_log 的（22.10–23.3 没有，那里使用
-- 22.10.1.0/ 中仅字节数的变体）。
SELECT
    toStartOfHour(event_time)                               AS time,
    hex(SHA256(concat(database, '%salt%')))                 AS database,
    hex(SHA256(concat(table, '%salt%')))                    AS table,
    status,
    count()                                                 AS flushes,
    sum(rows)                                               AS total_rows,
    sum(bytes)                                              AS total_bytes,
    -- 延迟（毫秒）：由 DateTime64(6) 的 *_microseconds 列通过浮点减法
    -- 计算 —— 与 22.10.1.0/ 这一级完全相同。
    round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)            AS avg_flush_ms,
    round(quantile(0.9)((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0)  AS p90_flush_ms
FROM system.asynchronous_insert_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time, database, table, status
ORDER BY time, database, table
