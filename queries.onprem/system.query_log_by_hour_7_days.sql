-- tier: essential
-- 每小时的查询数，按 kind、type 和错误码分组，每条查询只计一次——
-- 不做 ARRAY JOIN（query_log_details_7_days 会把一条查询按它涉及的每张表
-- 各重复一次）。dashboard 的 query-rate、per-kind、exception 和 Keeper-error
-- 面板都读这个文件，而不是再次查询 system.query_log。
SELECT
    toStartOfHour(event_time)                                    AS time,
    query_kind,
    type,
    exception_code,
    count()                                                      AS count,
    sum(query_duration_ms)                                       AS query_duration_ms,
    sum(memory_usage)                                            AS memory_usage,
    leftUTF8(anyIf(exception, exception_code != 0), 300)         AS exception
FROM system.query_log
WHERE event_time > {from:7d} AND event_time <= {to:now}
  AND type != 'QueryStart'
GROUP BY time, query_kind, type, exception_code
ORDER BY time
