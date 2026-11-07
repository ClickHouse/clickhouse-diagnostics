-- 这个 collector（采集器）回答以下问题：
--   * 一个“成功”却没有写入任何数据的 MV（read_rows > 0、written_rows = 0、
--     exception_code = 0）——静默丢行这一类。它在 query_log 中不可见，
--     因为父级 INSERT 报告成功。
--   * 基础 part 已提交后 MV 推送抛异常，导致
--     基础表和 MV 目标不一致（ExceptionWhileProcessing）。
--   * 每个视图随内存和持续时间的变化——用它识别某次升级使
--     单个 MV 的峰值内存升高数倍，而行数保持平稳。
--   * 链中两个视图之间的偏差，通过比较每个 bucket 的 written_rows 来判断。
SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    -- hostname 列在 23.11 加入；根文件没有主机归属信息。
    hostname,
    view_name,
    view_target,
    view_type,
    status,
    exception_code,
    count()                                             AS executions,
    -- 本身不算故障：SELECT 带 WHERE 子句的 MV 合法地
    -- 对大多数 block 不写入任何数据。这就是为什么它的分母（executions）和
    -- 行总计放在同一行——按每个视图随时间的比率来读。
    countIf(qvl.read_rows > 0 AND qvl.written_rows = 0) AS zero_write_executions,
    sum(qvl.read_rows)                                  AS read_rows,
    sum(qvl.read_bytes)                                 AS read_bytes,
    sum(qvl.written_rows)                               AS written_rows,
    sum(qvl.written_bytes)                              AS written_bytes,
    sum(qvl.view_duration_ms)                           AS view_duration_ms,
    max(qvl.view_duration_ms)                           AS max_view_duration_ms,
    -- 只看 sum() 会掩盖这类回归：总内存跟踪插入量，
    -- 所以会因无关原因波动。把中位数/最大值与平稳的
    -- written_rows 对照，才能识别出某个 MV 每个 block 变得更昂贵。
    avg(qvl.peak_memory_usage)                          AS avg_peak_memory_usage,
    quantile(0.5)(qvl.peak_memory_usage)                AS median_peak_memory_usage,
    max(qvl.peak_memory_usage)                          AS max_peak_memory_usage,
    -- 每个聚合参数都有意通过 `qvl.` 别名读取。
    -- `sum(view_duration_ms) AS view_duration_ms` 会把 view_duration_ms 放进
    -- 查询的 GLOBAL 别名作用域，因此后续裸写 max(view_duration_ms) 会解析到
    -- 那个聚合函数，服务器会拒绝整个查询："Aggregate function
    -- sum(view_duration_ms) AS view_duration_ms is found inside another aggregate
    -- function"（ILLEGAL_AGGREGATION 184，编写本文件时在 26.7.5.10 上遇到）。
    -- 加限定符可以完全绕开别名。
    leftUTF8(any(qvl.exception), 500)                   AS exception,
    min(qvl.event_time)                                 AS minDate,
    max(qvl.event_time)                                 AS maxDate
-- 使用 merge() 而不是 system.query_views_log：改变 schema 的升级会重命名旧的
-- 表为 query_views_log_0。只读活动表会静默丢失
-- 升级前基线——而这正是升级后回归分析需要的对比。
-- 当这样的表完全不存在时（log_query_views = 0，或未配置
-- <query_views_log>），merge() 会以错误码 636 CANNOT_EXTRACT_TABLE_STRUCTURE 失败，而不是
-- 60；这在 execution_log.txt 中表现为干净、可识别的“未采集”。
--
-- 故意不采集 view_uuid：已在 26.7.5.10 验证，ClickHouse
-- 会为每个视图写入零 UUID（TO 目标和 inner-table 都一样），
-- 而 system.tables 保存真实值。它会是一个常量列。对于
-- 没有 TO 的 MV，view_target 是 `db.\`.inner_id.<uuid>\``，并且会随
-- 视图重建而变化，因此这是用于观察重建的列。
FROM clusterAllReplicas(default, merge(system, '^query_views_log')) AS qvl
WHERE (event_time > {from:3d} AND event_time <= {to:now})
GROUP BY ALL
