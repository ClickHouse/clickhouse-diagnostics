-- tier: essential
-- 窗口：3 天。part_log 记录每个 part 事件（NewPart、MergeParts、DownloadPart、
-- RemovePart 等），在繁忙的副本上每周可达数十 GiB；3 天可在只扫描一小部分数据的情况下保留事故前后的基线。
SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    event_type,
    merge_reason,
    partition_id,
    hostname,
    concat(database, '.', table) AS table_name,
    error,
    any(pl.exception) AS exception,
    uniqExact(pl.exception) AS distinct_exceptions,
    sum(peak_memory_usage) AS peak_memory_usage,
    sum(duration_ms) AS duration_ms,
    sum(size_in_bytes) AS size_in_bytes,
    count() as count
FROM clusterAllReplicas(default, system.part_log) AS pl
-- 故意完全不采集 part_name，两种形式都不采集。
--
-- 把它作为 GROUP BY 键是致命的：它在每个 part 内唯一，因此以它为键会把
-- 这个“聚合”变成 system.part_log 的逐行副本（在玩具数据集上，868 个
-- 事件对应 868 行；生产环境 7 天会有数百万行）。这正是
-- 本文件要避免的回归，因此不要再把它加回键中。
--
-- 用 any(part_name) 采样则毫无用处：从组覆盖的任意多个 part 中
-- 取一个任意 part，却无法说明为什么选中它。
-- 它不是可用于排查的抓手。有用的粒度是
-- bucket x table x event_type x merge_reason x partition x error，
-- partition_id 是后续追踪用的标识符。
-- distinct_exceptions 是 any(exception) 的分母。由于
-- 错误码本身就是分组键，组内每行共享它，因此采样的
-- 消息具有代表性而非任意选择——但文本仍会随其嵌入的标识符
-- 变化，而且单条消息无法说明它
-- 代表 1 次失败还是 400 次相同失败。uniqExact 用一个
-- 整数给出答案，且不增加行：40 个事件 / 1 条不同消息表示一个反复出现的
-- 故障，40 / 40 则是四十个恰好共享同一错误码的不同故障。
--
-- any()/uniqExact() 有意通过 `pl.` 表别名读取该列。
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
WHERE (event_time > {from:3d} AND event_time <= {to:now})
GROUP BY ALL
