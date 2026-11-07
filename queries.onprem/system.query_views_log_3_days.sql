-- 这个 collector 回答以下问题：
--   * 一个 MV “成功”却什么都没写入（read_rows > 0、written_rows = 0、
--     exception_code = 0）——静默丢行这一类。它在 query_log 里不可见，
--     因为父 INSERT 报告的是成功。
--   * 一次 MV 推送抛异常，而基础 part 已经提交，导致
--     基表与 MV 目标不一致（ExceptionWhileProcessing）。
--   * 各视图随时间的内存与耗时——借此发现某次升级使
--     单个 MV 的峰值内存成倍上升，而其行数保持不变。
--   * 链中两个视图之间的偏离，通过对比每个 bucket 的 written_rows 发现。
SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    view_name,
    view_target,
    view_type,
    status,
    exception_code,
    count()                                             AS executions,
    -- 它本身并非故障：SELECT 带 WHERE 的 MV 在大多数 block 上本来就会
    -- 什么也不写。正因如此，它的分母（executions）与行合计才放在同一行——
    -- 请按每个视图随时间的比值来解读。
    countIf(qvl.read_rows > 0 AND qvl.written_rows = 0) AS zero_write_executions,
    sum(qvl.read_rows)                                  AS read_rows,
    sum(qvl.read_bytes)                                 AS read_bytes,
    sum(qvl.written_rows)                               AS written_rows,
    sum(qvl.written_bytes)                              AS written_bytes,
    sum(qvl.view_duration_ms)                           AS view_duration_ms,
    max(qvl.view_duration_ms)                           AS max_view_duration_ms,
    -- 只看 sum() 会掩盖这类回归：总内存随写入量变化，
    -- 因此它会因无关紧要的原因而波动。要识别单个 MV 的每 block 成本
    -- 是否上升，应看中位数/最大值与平稳 written_rows 的对比。
    avg(qvl.peak_memory_usage)                          AS avg_peak_memory_usage,
    quantile(0.5)(qvl.peak_memory_usage)                AS median_peak_memory_usage,
    max(qvl.peak_memory_usage)                          AS max_peak_memory_usage,
    -- 每个聚合参数都特意通过 `qvl.` 别名读取。
    -- `sum(view_duration_ms) AS view_duration_ms` 会把 view_duration_ms 放进
    -- 查询的 GLOBAL 别名作用域，于是后面裸写的 max(view_duration_ms) 会解析到
    -- 那个聚合函数，服务器拒绝整条查询："Aggregate function
    -- sum(view_duration_ms) AS view_duration_ms is found inside another aggregate
    -- function"（ILLEGAL_AGGREGATION 184，写这个文件时在 26.7.5.10 上触发）。
    -- 加限定名可以完全绕开别名。
    leftUTF8(any(qvl.exception), 500)                   AS exception,
    min(qvl.event_time)                                 AS minDate,
    max(qvl.event_time)                                 AS maxDate
-- 用 merge() 而不是 system.query_views_log：改变 schema 的升级会把旧表
-- 重命名为 query_views_log_0。只读活动表会静默丢掉
-- 升级前的基线——而这恰恰是判断升级后回归所需的对比。
-- 当这样的表完全不存在时（log_query_views = 0，或未配置
-- <query_views_log>），merge() 会以 code 636 CANNOT_EXTRACT_TABLE_STRUCTURE
-- 失败而非 code 60；这在 execution_log.txt 里是干净、可识别的 "not collected"。
--
-- view_uuid 被刻意不采集：已在 26.7.5.10 上验证，ClickHouse
-- 对每个视图（TO 目标和内部表都一样）都往这里写入零 UUID，
-- 而真正的值在 system.tables 里。那会是一个常量列。对于
-- 没有 TO 的 MV，view_target 是 `db.\`.inner_id.<uuid>\``，并且确实会在
-- 视图被重建时变化，所以要观察重建就盯这一列。
FROM merge(system, '^query_views_log') AS qvl
WHERE (event_time > {from:3d} AND event_time <= {to:now})
-- 用显式键列表而不是 GROUP BY ALL：后一种语法需要 22.12+，而这个
-- 根文件必须在所有受支持的服务器（22.8+）上运行。
GROUP BY time, view_name, view_target, view_type, status, exception_code
