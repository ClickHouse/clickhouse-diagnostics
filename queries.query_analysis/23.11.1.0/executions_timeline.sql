-- 23.11+ 变体：使用 hostname 列（23.11 新增到 system 日志表）；根文件改用 hostName()。
-- 窗口内目标 hash 的每次单独执行对应一行。
-- 驱动 dashboard 中的逐次执行散点图，使每次运行
-- 都显示为独立的点（当例如慢查询的全部 10 次执行
-- 都发生在同一小时内时，按小时分桶的 hash_summary 会
-- 掩盖这一点）。
--
-- 对于流量极高的查询，LIMIT 10000 限制输出；
-- ORDER BY event_time DESC 意味着保留最近 10000 条，
-- 这通常正是排查所关心的。
-- 当设置了 --normalized-query-hash 时运行（由 --query-id 自动推导）。
SELECT
    toString(event_time)                                    AS ts,
    event_time_microseconds                                 AS ts_micro,
    query_id,
    type,
    exception_code,
    query_duration_ms,
    memory_usage,
    formatReadableSize(memory_usage)                        AS memory_usage_human,
    -- 每次执行的 user CPU，单位为微秒 → JS 在逐次执行散点图中
    -- 将其渲染为秒。使用 coalesce 是因为并非每个
    -- ProfileEvents map 都有 UserTimeMicroseconds（例如从未在
    -- 用户态消耗 CPU 的极短查询）。
    ProfileEvents['UserTimeMicroseconds']                   AS user_cpu_us,
    read_rows,
    read_bytes,
    formatReadableSize(read_bytes)                          AS read_bytes_human,
    hostname,
    user
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
ORDER BY event_time DESC
LIMIT 10000
