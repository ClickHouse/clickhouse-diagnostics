-- 目标 hash 的每分钟执行次数。dashboard 的
-- count 图表过去按小时分桶，但这掩盖了小时内的
-- 执行分布；按分钟分桶提供更细的粒度，
-- 又不会在典型的排查时间窗口内使行数爆炸。
--
-- 基于值的指标（duration、memory、user CPU、read rows/bytes）
-- 现在改由 executions_timeline.sql 驱动——每次
-- 执行一行，而不是聚合。因此 hash_summary 现在只承载
-- 计数拆分（succeeded vs failed），这是唯一一个
-- 按每次执行没有意义的图表。
-- 当设置了 --normalized-query-hash 时运行（由 --query-id 自动推导）。
SELECT
    toString(toStartOfMinute(event_time))                   AS time_bucket,
    count()                                                 AS executions,
    countIf(type = 'QueryFinish')                           AS succeeded,
    countIf(type != 'QueryFinish' AND type != 'QueryStart') AS failed
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
GROUP BY time_bucket
ORDER BY time_bucket
