-- tier: essential
SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    query_kind,
    tables,
    splitByChar('.', tables)[1] as database,
    splitByChar('.', tables)[2] as table,
    type,
    user,
    sum(memory_usage) as memory_usage,
    sum(result_rows) as result_rows,
    sum(result_bytes) as result_bytes,
    sum(written_bytes) as written_bytes,
    sum(written_rows) as written_rows,
    sum(read_rows) as read_rows,
    sum(read_bytes) as read_bytes,
    sum(query_duration_ms) as query_duration_ms,
    interface,
    normalized_query_hash,
    count(*) as count,
    -- 截断以保持归档较小，但保留它：没有它，
    -- 本文件中的 normalized_query_hash 就无法在服务器
    -- 消失后映射回 SQL。
    --
    -- 用 leftUTF8 而不是 left：left() 按字节计数，截断落在
    -- 多字节字符中间时会输出一个孤立的续字节，导致 .jsonl
    -- 行不再是有效 UTF-8——服务器响应会原样写入
    -- 磁盘（internal/query/executor.go），因此下游没有任何环节
    -- 会修复它。严格解析器会直接拒绝该行
    -- （python json: "invalid continuation byte"）；jq 较宽松，
    -- 会静默替换为 U+FFFD，对文本的破坏同样严重，
    -- 只是更隐蔽。客户 SQL 中任何非 ASCII 字面量都可能触发
    -- （'München'、CJK 标识符）。leftUTF8 按码点计数，并
    -- 在 22.8 与 left 一同注册，因此不需要版本门槛。
    leftUTF8(any(query), 500) as query,
    min(event_time) as minDate,
    max(event_time) as maxDate,
    exception_code,
    any(ql.exception) as exception,
    uniqExact(ql.exception) as distinct_exceptions
FROM clusterAllReplicas(default, system.query_log) AS ql
LEFT ARRAY JOIN tables
-- distinct_exceptions 是 any(exception) 的分母。由于
-- 错误码本身就是分组键，组内每行共享它，因此采样的
-- 消息具有代表性而非任意选择——但文本仍会随其嵌入的标识符
-- 变化，而且单条消息无法说明它
-- 代表 1 次失败还是 400 次相同失败。uniqExact 用一个
-- 整数给出答案，且不增加行：40 个事件 / 1 条不同消息表示一个反复出现的
-- 故障，40 / 40 则是四十个恰好共享同一错误码的不同故障。
--
-- any()/uniqExact() 有意通过 `ql.` 表别名读取该列。
-- `any(exception) AS exception` 会把 `exception` 放进查询的别名作用域，
-- 而别名是全局的，所以裸写 `uniqExact(exception)` 会解析到那个
-- 聚合函数而不是列，服务器会直接拒绝该查询：
-- "Aggregate function any(exception) AS exception is found inside another
-- aggregate function"（ILLEGAL_AGGREGATION 184，在 26.7 上遇到）。重排
-- 投影没有帮助。加限定符可以完全绕开别名；已在
-- 22.8.21.38、23.7.5.30 和 26.7.5.10 上验证。
--
-- 故意不作为分组键。已在 26.7 验证：8 个 UNKNOWN_DATABASE 事件
-- 产生了 8 条不同消息（每条都点名自己的数据库），而按
-- 文本分组把 13 个聚合事件变成了 13 行——成了源表的逐行
-- 副本，和 part_name 遇到的同一个陷阱。
WHERE (event_time > {from:7d} AND event_time <= {to:now})
-- 使用 LEFT ARRAY JOIN 而不是 ARRAY JOIN：从未解析到表的查询有一个
-- 空的 `tables` 数组，普通 ARRAY JOIN 会完全丢弃这些行。这
-- 正是值得归档的失败类别——UNKNOWN_TABLE (60)、解析
-- 错误（6/27），以及分析完成前抛出的任何错误。LEFT 会保留它们，
-- 且 tables = ''。
--
-- 无论哪种方式，array join 的后果都是：涉及 N 张表的查询会发出 N
-- 行，并且每个 sum 都会在每一行上完整重复。读取汇总前
-- 请过滤到单张表；绝不要跨行再次求和，否则会重复计数。
GROUP BY ALL
