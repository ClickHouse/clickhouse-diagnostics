-- tier: essential
-- 当前正在进行的 part 拉取：本节点正从哪个 replica 拉取、
-- 进度多少、大小几何。Keeper 或网络事故之后出现大量长时间拉取，
-- 说明 replica 正在追赶；若两次 bundle 中某个拉取卡在同一进度，
-- 则是一条卡死的 interserver 连接。
SELECT
    database,
    table,
    elapsed,
    progress,
    result_part_name,
    partition_id,
    total_size_bytes_compressed,
    bytes_read_compressed,
    source_replica_hostname,
    source_replica_port,
    to_detached
FROM system.replicated_fetches
ORDER BY elapsed DESC
LIMIT 500
