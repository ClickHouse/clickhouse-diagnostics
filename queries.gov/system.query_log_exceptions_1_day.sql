-- tier: essential
-- 每小时、按错误码统计的失败查询，每个查询只计一次。
-- query_log_details_7_days 会按查询触及的每张表重复一次（LEFT
-- ARRAY JOIN tables），所以从它那里读到的计数要么翻倍，要么去重后
-- 把同一模式在不同表上的两次执行合并；
-- 异常告警（high_exception_rate、keeper_exception_spike、
-- too_many_simultaneous_queries）改为读取这里的精确计数。只读取失败
-- 行；一天足以覆盖它们 24 小时的回看窗口。
-- Gov：不含异常文本（它会嵌入标识符和字面量）。
SELECT
    toStartOfHour(event_time)  AS time,
    type,
    exception_code,
    count()                    AS count
FROM system.query_log
WHERE event_time > {from:1d} AND event_time <= {to:now}
  AND exception_code != 0
GROUP BY time, type, exception_code
ORDER BY time, exception_code
