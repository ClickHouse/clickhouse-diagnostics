-- tier: essential
-- 每小时的失败查询数及错误码，每条查询只计一次。
-- query_log_details_7_days 会把一条查询按它涉及的每张表各重复一次
-- （LEFT ARRAY JOIN tables），所以从它读出的计数要么翻倍，要么去重后
-- 把同一模式在不同表上的两次执行合并在一起；
-- 异常类告警（high_exception_rate、keeper_exception_spike、
-- too_many_simultaneous_queries）改为读取这个精确计数。这里只读取失败
-- 的行；一天足以覆盖它们 24 小时的回看窗口。
SELECT
    toStartOfHour(event_time)  AS time,
    type,
    exception_code,
    count()                    AS count,
    any(exception)             AS exception
FROM system.query_log
WHERE event_time > {from:1d} AND event_time <= {to:now}
  AND exception_code != 0
GROUP BY time, type, exception_code
ORDER BY time, exception_code
