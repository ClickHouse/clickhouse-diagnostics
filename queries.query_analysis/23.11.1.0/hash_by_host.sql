-- 23.11+ 变体：使用 hostname 列（23.11 新增到 system 日志表）；根文件改用 hostName()。
-- 目标 hash 的按 host 分布。在云集群中揭示
-- "某个节点比其余节点慢" 的模式。
-- 当设置了 --normalized-query-hash 时运行（由 --query-id 自动推导）。
SELECT
    hostname,
    count()                                                 AS executions,
    max(query_duration_ms)                                  AS max_duration_ms,
    min(query_duration_ms)                                  AS min_duration_ms,
    round(avg(query_duration_ms), 0)                        AS avg_duration_ms,
    round(quantile(0.95)(query_duration_ms), 0)             AS p95_duration_ms,
    formatReadableSize(round(avg(memory_usage), 0))         AS avg_memory,
    formatReadableSize(max(memory_usage))                   AS max_memory,
    countIf(exception_code != 0)                            AS errors
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
GROUP BY hostname
ORDER BY max_duration_ms DESC
