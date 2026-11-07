-- tier: essential
-- 存在哪些策略以及它们由哪些磁盘支撑。结合 system.disks.type
-- （ObjectStorage 与 Local）可说明表数据是否位于 S3/Azure/GCS——
-- 这是每个“文件不存在 / key 不存在”finding 的前提。
SELECT
    policy_name,
    volume_name,
    volume_priority,
    disks,
    max_data_part_size,
    move_factor,
    prefer_not_to_merge
FROM system.storage_policies
ORDER BY policy_name, volume_priority
