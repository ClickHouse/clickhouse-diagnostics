-- 过去一天哪些 Keeper 路径承载了负载（覆盖每个副本），
-- 以及哪些在增长：
-- 请求最多的 50 个路径类别，加上净创建最多的 50 个
-- 类别（creates − removes），每个类别一行。数据来自
-- system.aggregated_zookeeper_log（自 25.9 起默认开启）。
--   path        父路径，其中每个纯数字段替换为 N，
--               这样数千个子路径（…/_objects/15536/.dir、
--               …/block_numbers/20260918）会归为一个类别；表 UUID
--               和名称保留——它们说明是“哪张”表（shard 编号
--               例如 /tables/01/ 也会变成 N）
--   raw_paths   该类别合并了多少个不同路径
--   ops         该类别上的每个请求，包括 Multi 内部的
--               子请求（批量读取风暴就在其中）；in_multi 是
--               来自 Multi / MultiRead 内部的部分
--   creates / removes / net_creates   成功的 create 和 remove 请求
--               （CreateIfNotExists 返回 ZNODEEXISTS，或 Remove 返回
--               ZNONODE 时没有改变任何东西，不计入）。net_creates 是
--               请求平衡，不是 znode 计数：一次 RemoveRecursive 会删除
--               整个子树。creates 远高于 removes 的类别
--               与增长一致——请用 keeper/<member>.txt 中的 zk_znode_count
--               确认
--   avg_latency_us  加权，仅统计独立请求
--   replicas    该类别中任一路径被使用过的最大副本数
-- 开销：扫描一天的数据，先按 raw path 分组（内存取决于
-- 不同路径数，而不是行数），仅对不同路径做归一化。
-- Multi / MultiRead 外层不携带 path，已排除。
-- 操作按 Enum16 编码比较，而不是按名称——查询检查会拒绝
-- 注释之外的任何 CREATE 单词：creates = Create (1)、
-- CreateIfNotExists (502)；removes = Remove (2)、RemoveRecursive (503)、
-- TryRemove (505)；reads = Exists (3)、Get (4)、List (12)、FilteredList
-- (500)、FilteredListWithStatsAndData (506)、ListRecursive (507)。
-- 仅限特定版本（26.3+，需要 `is_subrequest`）：更旧的服务器会跳过本文件。
SELECT path, ops, in_multi, creates, removes, net_creates, reads, avg_latency_us, raw_paths, replicas
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
        max(p_replicas)                                AS replicas,
        row_number() OVER (ORDER BY ops DESC)          AS rank_ops,
        row_number() OVER (ORDER BY net_creates DESC)  AS rank_growth
    FROM
    (
        SELECT
            replaceRegexpAll(replaceRegexpAll(parent_path, '/[0-9]+(/|$)', '/N\\1'), '/[0-9]+(/|$)', '/N\\1') AS path,
            p_ops, p_in_multi, p_creates, p_removes, p_reads, p_lat, p_top_ops, p_replicas
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
                sumIf(count, is_subrequest = 0)                                         AS p_top_ops,
                uniq(hostname)                                                          AS p_replicas
            FROM clusterAllReplicas(default, system.aggregated_zookeeper_log)
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
