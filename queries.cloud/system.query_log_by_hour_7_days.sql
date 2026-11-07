-- tier: essential
-- 每小时、按 kind、type 和错误码统计的查询数，每个查询只计一次——
-- 不使用 ARRAY JOIN（query_log_details_7_days 会按查询涉及的每张表
-- 重复一次）。dashboard 的 query-rate、per-kind、exception 和 Keeper-error
-- 面板读取本文件，而不是再次查询 system.query_log。
SELECT
    toStartOfHour(event_time)                                    AS time,
    query_kind,
    type,
    exception_code,
    count()                                                      AS count,
    sum(query_duration_ms)                                       AS query_duration_ms,
    sum(memory_usage)                                            AS memory_usage,
    leftUTF8(anyIf(exception, exception_code != 0), 300)         AS exception
FROM clusterAllReplicas(default, system.query_log)
WHERE event_time > {from:7d} AND event_time <= {to:now}
  AND type != 'QueryStart'
GROUP BY time, query_kind, type, exception_code
ORDER BY time
