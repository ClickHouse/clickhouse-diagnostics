-- tier: essential
SELECT
    hostName() AS hostname,
    name,
    code,
    value,
    toString(last_error_time)    AS last_error_time,
    last_error_message,
    left(toString(last_error_trace), 500)  AS last_error_trace
FROM clusterAllReplicas(default, system.errors)
WHERE value > 0
-- 每个副本 100 条，而不是整个集群 100 条：dashboard 会汇总
-- 各副本按错误码的计数器，若在汇总前做全集群截断，
-- 可能丢掉整体上最频繁的错误。
ORDER BY value DESC
LIMIT 100 BY hostname
