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
    -- 做了截断，以便诊断包保持小巧，但仍会保留：没有它，这个文件里的
    -- normalized_query_hash 就无法再映射回 SQL——一旦服务器不存在，
    -- 这条线索就断了。
    --
    -- 用 leftUTF8 而不是 left：left() 按 BYTES 计数，所以截断点落在
    -- 多字节字符中间时会留下一个孤立的续字节，.jsonl 行就不再是
    -- 合法 UTF-8——服务器的响应是原样写入磁盘的
    -- （internal/query/executor.go），下游没有任何环节会修复它。
    -- 严格解析器会直接拒绝该行（python json：
    -- "invalid continuation byte"）；jq 比较宽松，会静默替换成
    -- U+FFFD，同样破坏文本，只是不声张。客户 SQL 里任何非 ASCII
    -- 字面量都会触发（'München'、CJK 标识符）。leftUTF8 按码点计数，
    -- 且在 22.8 就与 left 一同注册，
    -- 因此不需要版本判断。
    leftUTF8(any(query), 500) as query,
    min(event_time) as minDate,
    max(event_time) as maxDate,
    exception_code,
    any(ql.exception) as exception,
    uniqExact(ql.exception) as distinct_exceptions
FROM system.query_log AS ql
LEFT ARRAY JOIN tables
-- distinct_exceptions 是 any(exception) 的分母。由于错误码本身就是
-- 分组键，同一组内每一行都共享它，所以抽样得到的消息具有代表性
-- 而非随意选取——但文本仍会随它内嵌的标识符而变化，一条消息也无法
-- 说明它代表的是 1 次失败还是 400 次完全相同的失败。
-- uniqExact 用一个整数给出这一点，且不增加额外行：
-- 40 个事件 / 1 条不同消息是一次反复出现的故障，
-- 40 / 40 则是四十次不同故障，只是恰好共享同一个错误码。
--
-- any()/uniqExact() 特意通过 `ql.` 表别名读取该列。
-- `any(exception) AS exception` 会把 `exception` 放进查询的别名作用域，
-- 而别名是全局的，所以裸写 `uniqExact(exception)` 会解析到那个聚合函数
-- 而不是该列，服务器会直接拒绝这条查询：
-- "Aggregate function any(exception) AS exception is found inside another
-- aggregate function"（ILLEGAL_AGGREGATION 184，在 26.7 上触发）。
-- 调整投影顺序也没有用。加限定名可以完全绕开别名；已在
-- 22.8.21.38、23.7.5.30 和 26.7.5.10 上验证。
--
-- 特意不作为分组键。已在 26.7 上验证：8 个 UNKNOWN_DATABASE 事件
-- 产生了 8 条不同消息（每条都写出自己的数据库名），若按文本分组，
-- 13 个已聚合的事件会变成 13 行——逐行照抄源数据，
-- 正是 part_name 踩过的坑。
WHERE (event_time > {from:7d} AND event_time <= {to:now})
-- 用 LEFT ARRAY JOIN 而不是 ARRAY JOIN：从未解析出表的查询其
-- `tables` 数组为空，而普通 ARRAY JOIN 会把这些行整个丢掉。
-- 这恰恰是值得归档的那类失败——UNKNOWN_TABLE (60)、
-- 解析错误 (6/27)，以及任何在分析完成前就抛出的错误。
-- LEFT 会保留它们，tables = ''。
--
-- 无论如何，数组 join 都带来一个后果：一条查询涉及 N 张表就会产生
-- N 行，且每个 sum 在每一行上都被完整重复。读取合计值前先过滤到
-- 单张表；绝不要跨行再次求和，否则会重复计数。
-- 用显式键列表而不是 GROUP BY ALL：后一种语法需要 22.12+，
-- 而这个根文件必须在所有受支持的服务器（22.8+）上运行。
GROUP BY time, query_kind, tables, database, table, type, user,
         interface, normalized_query_hash, exception_code
