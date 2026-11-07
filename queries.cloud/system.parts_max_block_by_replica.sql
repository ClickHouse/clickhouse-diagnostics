-- SharedMergeTree 集群上每个副本的 part 传播延迟：副本在哪些
-- 分区上落后、落后多少个 block，以及哪些分区完全没拿到。
-- 每个副本保留自己的 system.parts 视图，并异步获知新 part
-- （Keeper 通知，然后 fetch）；仍缺少某个分区最新 part 的副本
-- 会显示比同类更低的 max_block_number，而某个分区一个 part 都没
-- fetch 到的副本根本不会贡献行——这就是为什么副本集合会被
-- 显式比较（见下文的
-- replicas_missing_partition）。按
-- (database, table, partition_id) 聚合，因为 block 编号按分区
-- 分配。
--
-- 为什么不用 system.replicas：该表按副本说明它还有多少个 level-0
-- part 未 fetch（inserts_in_queue），以及其中最老的一个有多旧
-- （absolute_delay、inserts_oldest_time）——这是服务器自己的答案，
-- 也是回答“多久”时应使用的值。它不说明每个副本到达了哪个分区或
-- 哪个 block，这正是本文件补充的内容。
--
-- 容易被过度解读的列：
--   * blocks_behind 是可靠的：block 编号在每个分区内单调递增，
--     因此集群最大值减去该副本的最大值，就是它未拿到的 block
--     的真实数量。
--   * newest_part_skew_s 不是这些缺失 block 的年龄。它是两个
--     独立取最大值的 modification_time 之间的差距，会以三种
--     方式误导：空闲一段时间后它很大，即使缺失的 block 只有
--     几秒之久；对于几秒内写入的一批数据，无论副本落后多久它
--     都很小；而落后副本上的一次 merge 会刷新其
--     modification_time，却不缩小 block 差距。把它读作每个副本
--     持有的最新 part 之间的偏差——可作为佐证，绝不能当作
--     持续时间。持续时间在
--     system.replicas.absolute_delay 中。
--   * replicas_missing_partition 列出在此分区中没有活动 part 的
--     副本，计算方式是每张表的副本集合减去每个分区的
--     副本集合。对于最近的分区，这是最强的延迟信号。对于旧分区，
--     它可能是下文每主机上限造成的假象：该上限保留每个副本最近写入
--     的分区并丢弃其余部分：把 partitions_sampled_for_host 与上限
--     比较。
--
-- 开销，以及有意避免的两种形态：
--   * 使用窗口函数，而不是与自身 join 的 CTE——WITH … AS (subquery)
--     会在每次引用处内联，因此读取两次的 CTE 会让 system.parts
--     扫描两次（测得：2 倍行数，7 倍内存）；
--   * 在 fan-out 内部使用 LIMIT 20000 BY hostname——每个副本
--     贡献其最近写入的 20 000 个分区（落后副本的热点分区仍在
--     它自己的最近分区中），因此发起方在窗口步骤最多持有
--     副本数 × 20 000 行，而不是每个副本的所有
--     分区。
-- 只返回落后或不完整的行，最差的在前，上限为
-- 1000：cloud 采集时文件为空表示当时没有副本落后，
-- 也没有副本缺少分区。原始数字在
-- parts_max_block_recent 中。
SELECT
    hostname,
    database,
    table,
    partition_id,
    max_block,
    cluster_max_block,
    cluster_max_block - max_block                                   AS blocks_behind,
    last_part_time,
    cluster_last_part_time,
    dateDiff('second', last_part_time, cluster_last_part_time)      AS newest_part_skew_s,
    active_parts,
    replicas_with_partition,
    replicas_with_table,
    replicas_missing_partition,
    partitions_sampled_for_host
FROM
(
    SELECT
        *,
        max(max_block)        OVER w_part AS cluster_max_block,
        max(last_part_time)   OVER w_part AS cluster_last_part_time,
        count()               OVER w_part AS replicas_with_partition,
        uniqExact(hostname)   OVER w_tab  AS replicas_with_table,
        -- 持有该表但在该分区中没有活动 part 的
        -- 副本。使用 groupUniqArray 作为窗口聚合，因此对 fan-out
        -- 仍然只扫描一遍。
        arrayFilter(h -> NOT has(groupUniqArray(hostname) OVER w_part, h),
                    groupUniqArray(hostname) OVER w_tab)            AS replicas_missing_partition,
        count()               OVER w_host AS partitions_sampled_for_host
    FROM
    (
        SELECT
            hostName()               AS hostname,
            database,
            table,
            partition_id,
            max(max_block_number)    AS max_block,
            count()                  AS active_parts,
            max(modification_time)   AS last_part_time
        FROM clusterAllReplicas(default, system.parts)
        WHERE active
          AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
        GROUP BY hostname, database, table, partition_id
        ORDER BY hostname, last_part_time DESC
        LIMIT 20000 BY hostname
    )
    WINDOW
        w_part AS (PARTITION BY database, table, partition_id),
        w_tab  AS (PARTITION BY database, table),
        w_host AS (PARTITION BY hostname)
)
WHERE blocks_behind > 0
   OR notEmpty(replicas_missing_partition)
-- parts_propagation_lag alert 统计的行排在最前，因此上限
-- 不会因旧噪声切掉它们：落后 >= 32 个 block 的副本，然后
-- 缺失分区按最新优先（alert 统计过去一天有写入的分区，
-- 因此最近的分区总是排在旧分区之前）。
ORDER BY blocks_behind >= 32 DESC,
         notEmpty(replicas_missing_partition) DESC,
         cluster_last_part_time DESC,
         blocks_behind DESC, newest_part_skew_s DESC
LIMIT 1000
