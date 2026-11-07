-- 过去一天中哪些 Keeper 路径承担了负载、哪些在增长：
-- 请求最多的 50 个路径类别，加上净创建（creates − removes）最多的
-- 50 个，每类一行。数据来自
-- system.aggregated_zookeeper_log（自 25.9 起默认开启）。
--   path        父路径，所有纯数字段都替换成 N，
--               这样成千上万的子项（…/_objects/15536/.dir、
--               …/block_numbers/20260918）会归并成一个类别；表 UUID
--               和名称会保留——它们说明是哪张表（像 /tables/01/
--               这样的 shard 编号也会变成 N）
--   raw_paths   该类别合并了多少个不同路径
--   ops         该类别上的所有请求，包含 Multi 内部的
--               子请求（批量读取风暴就在这里）；in_multi 是
--               其中来自 Multi / MultiRead 的部分
--   creates / removes / net_creates   成功的 create 和 remove 请求
--               （CreateIfNotExists 得到 ZNODEEXISTS、或 Remove 得到
--               ZNONODE，都不改变任何东西，不计入）。net_creates 是
--               请求上的收支，不是 znode 数量：一次 RemoveRecursive 会删掉
--               整棵子树。creates 远高于 removes 的类别
--               与增长相符——可用 keeper/<member>.txt 中的
--               zk_znode_count 确认
--   avg_latency_us  加权，只统计独立请求
-- 成本：扫描这一天一次，先按原始路径分组（内存按
-- 不同路径数而非行数计），只在去重后的路径上做归一化。
-- Multi / MultiRead 信封不携带路径，会被排除。
-- 操作按 Enum16 代码比较，而不是按名称——查询检查会拒绝注释之外
-- 出现的 CREATE 一词：creates = Create (1)、
-- CreateIfNotExists (502)；removes = Remove (2)、RemoveRecursive (503)、
-- TryRemove (505)；reads = Exists (3)、Get (4)、List (12)、FilteredList
-- (500)、FilteredListWithStatsAndData (506)、ListRecursive (507)。
-- 仅版本目录（26.3+，需要 `is_subrequest`）：更旧的服务器会跳过这个文件。
SELECT path, ops, in_multi, creates, removes, net_creates, reads, avg_latency_us, raw_paths
FROM
(
    SELECT
        path,
        sum(p_ops)                                     AS ops,
        sum(p_in_multi)                                AS in_multi,
        sum(p_creates)                                 AS creates,
        sum(p_removes)                                 AS removes,
        toInt64(creates) - toInt64(removes)            AS net_creates,
        sum(p_reads)                                   AS reads,
        round(sum(p_lat) / nullIf(sum(p_top_ops), 0))  AS avg_latency_us,
        count()                                        AS raw_paths,
        row_number() OVER (ORDER BY ops DESC)          AS rank_ops,
        row_number() OVER (ORDER BY net_creates DESC)  AS rank_growth
    FROM
    (
        SELECT
            replaceRegexpAll(replaceRegexpAll(parent_path, '/[0-9]+(/|$)', '/N\\1'), '/[0-9]+(/|$)', '/N\\1') AS path,
            p_ops, p_in_multi, p_creates, p_removes, p_reads, p_lat, p_top_ops
        FROM
        (
            SELECT
                parent_path,
                sum(count)                                                              AS p_ops,
                sumIf(count, is_subrequest = 1)                                         AS p_in_multi,
                sumIf(greatest(toInt64(count) - toInt64(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))), 0), toInt16(operation) IN (1, 502))      AS p_creates,
                sumIf(greatest(toInt64(count) - toInt64(arraySum(mapValues(mapFilter((code, n) -> code != 'ZOK', errors)))), 0), toInt16(operation) IN (2, 503, 505)) AS p_removes,
                sumIf(count, toInt16(operation) IN (3, 4, 12, 500, 506, 507))          AS p_reads,
                sumIf(average_latency * count, is_subrequest = 0)                       AS p_lat,
                sumIf(count, is_subrequest = 0)                                         AS p_top_ops
            FROM system.aggregated_zookeeper_log
            WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
              AND event_time > {from:1d} AND event_time <= {to:now}
              AND parent_path != ''
            GROUP BY parent_path
        )
    )
    GROUP BY path
)
WHERE rank_ops <= 50 OR rank_growth <= 50
ORDER BY ops DESC
