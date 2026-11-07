-- 最近一天哪些 Keeper 路径承担了负载，哪些在增长
-- —— gov 变体。路径会指名数据库、表和副本，所以这些都不能
-- 离开服务器：每条路径被归约为它的形状（属于 ClickHouse
-- 自身 Keeper 词汇的段保留，纯数字段变为 N，
-- 其余变为 *），再加上 path_id，即规范化路径的加盐哈希，
-- 它能区分同形状的两张表而不点名它们。
-- 取请求最多的 50 个类别加上净创建最多的 50 个
-- （creates − removes），每类一行。构建自
-- system.aggregated_zookeeper_log（自 25.9 默认开启）。
--   ops         该类上的所有请求，包含 Multi 内部的子请求；
--               in_multi 是来自 Multi / MultiRead 内部的部分
--   creates / removes / net_creates   成功的创建和删除请求
--               （CreateIfNotExists 返回 ZNODEEXISTS，或 Remove 返回
--               ZNONODE，都不改变任何东西，不计入）。net_creates 是
--               请求收支，不是 znode 数量：一次 RemoveRecursive 会删除
--               整棵子树。creates 远高于 removes 的类别
--               与增长一致 —— 用 keeper/<member>.txt 中的
--               zk_znode_count 确认
--   avg_latency_us  加权，仅独立请求
--   raw_paths   该类合并了多少条不同路径
-- Multi / MultiRead 信封不携带路径，已排除。
-- 操作按 Enum16 代码比较，而不是按名称 —— 查询检查会拒绝
-- 注释之外任何位置出现 CREATE 一词：creates = Create (1)、
-- CreateIfNotExists (502)；removes = Remove (2)、RemoveRecursive (503)、
-- TryRemove (505)；reads = Exists (3)、Get (4)、List (12)、FilteredList
-- (500)、FilteredListWithStatsAndData (506)、ListRecursive (507)。
-- 仅版本目录（26.3+，需要 `is_subrequest`）：旧服务器跳过本文件。
SELECT shape, path_id, ops, in_multi, creates, removes, net_creates, reads, avg_latency_us, raw_paths
FROM
(
    SELECT
        arrayStringConcat(arrayMap(seg -> if(seg = '' OR seg = 'N' OR seg IN (
            'clickhouse', 'tables', 'replicas', 'shared', 'zero_copy', 'zero_copy_s3', 'zero_copy_hdfs', 'mergetree',
            'blocks', 'async_blocks', 'deduplication_hashes', 'block_numbers', 'temp', 'quorum', 'parallel', 'pinned_part_uuids',
            'log', 'queue', 'parts', 'columns', 'metadata', 'mutations', 'leader_election', 'is_active', 'is_lost', 'host',
            'log_pointer', 'flags', 'max_processed_insert_time', 'min_unprocessed_insert_time', 'mutation_pointer',
            'lost_part_count', 'alter_partition_version', 'nonincrement_block_numbers', 'last_part',
            'outdated_parts', 'condemned_parts', 'parts_update_leaders', 'parts_cleanup', 'part_moves_shard',
            '_objects', '_deleted_objects', '_transactions', '_blob_manager', 'dead', '.dir',
            'task_queue', 'ddl', 'active', 'finished', 'synced', 'max_log_ptr', 'counter', 'logs', 'first_replica_database_name',
            'keeper', 'api_version', 'feature_flags', 'session_id'), seg, '*'), splitByChar('/', path)), '/') AS shape,
        hex(SHA256(concat(path, '%salt%')))            AS path_id,
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
