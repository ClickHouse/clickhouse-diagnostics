-- tier: essential
-- 每小时、按错误码统计的失败查询，每个查询只计一次。
-- query_log_details_7_days 会按查询涉及的每张表重复一次（LEFT
-- ARRAY JOIN tables），因此从它读取的计数要么被翻倍，要么在去重后
-- 把同一模式在不同表上的两次执行合并；
-- 异常 alert（high_exception_rate、keeper_exception_spike、
-- too_many_simultaneous_queries）改为读取这个精确计数。只读取失败
-- 行；一天数据覆盖它们 24 小时的回看窗口。
SELECT
    toStartOfHour(event_time)  AS time,
    type,
    exception_code,
    count()                    AS count,
    any(exception)             AS exception
FROM clusterAllReplicas(default, system.query_log)
WHERE event_time > {from:1d} AND event_time <= {to:now}
  AND exception_code != 0
GROUP BY time, type, exception_code
ORDER BY time, exception_code
