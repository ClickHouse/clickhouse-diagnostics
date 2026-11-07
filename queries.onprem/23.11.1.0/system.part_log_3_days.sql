-- tier: essential
-- 窗口：3 天。part_log 记录每一次 part 事件（NewPart、MergeParts、DownloadPart、
-- RemovePart …），在繁忙的 replica 上每周可达数十 GiB；三天既能在事故前后保留基线，扫描量又只是其中一小部分。
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
FROM system.part_log AS pl
-- part_name 被刻意完全不采集，两种形式都不采。
--
-- 作为 GROUP BY 键是灾难性的：它每个 part 唯一，所以拿它作键会把
-- 这个“聚合”变成 system.part_log 的逐行副本（玩具数据集上 868 行对应
-- 868 个事件；生产环境 7 天则是数百万行）。这正是本文件要避免的
-- 回归，所以不要再把它加回键里。
--
-- 若用 any(part_name) 抽样，它只是没用：从该组涵盖的众多 part 里
-- 随便取一个，却无法说明为什么选中它。
-- 它不是一个可用于排查的抓手。有用的粒度是
-- bucket x table x event_type x merge_reason x partition x error，
-- 而 partition_id 才是应当继续追查的标识符。
-- distinct_exceptions 是 any(exception) 的分母。由于错误码本身就是
-- 分组键，同一组内每一行都共享它，所以抽样得到的消息具有代表性
-- 而非随意选取——但文本仍会随它内嵌的标识符而变化，一条消息
-- 也无法说明它代表的是 1 次失败还是 400 次完全相同的失败。
-- uniqExact 用一个整数给出这一点，且不增加额外行：
-- 40 个事件 / 1 条不同消息是一次反复出现的故障，
-- 40 / 40 则是四十次不同故障，只是恰好共享同一个错误码。
--
-- any()/uniqExact() 特意通过 `pl.` 表别名读取该列。
-- `any(exception) AS exception` 会把 `exception` 放进查询的别名作用域，
-- 而别名是全局的，所以裸写 `uniqExact(exception)` 会解析到那个聚合
-- 函数而不是该列，服务器会直接拒绝这条查询：
-- "Aggregate function any(exception) AS exception is found inside another
-- aggregate function"（ILLEGAL_AGGREGATION 184，在 26.7 上触发）。
-- 调整投影顺序也没有用。加限定名可以完全绕开别名；
-- 已在 22.8.21.38、23.7.5.30 和 26.7.5.10 上验证。
--
-- 特意不作为分组键。已在 26.7 上验证：8 个 UNKNOWN_DATABASE 事件
-- 产生了 8 条不同消息（每条都写出自己的数据库名），若按文本分组，
-- 13 个已聚合的事件会变成 13 行——逐行照抄源数据，
-- 正是 part_name 踩过的坑。
WHERE (event_time > {from:3d} AND event_time <= {to:now})
GROUP BY ALL
