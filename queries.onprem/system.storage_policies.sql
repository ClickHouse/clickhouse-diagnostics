-- tier: essential
-- 存在哪些策略、由哪些磁盘支撑。结合 system.disks.type
-- （ObjectStorage vs Local）即可判断表数据是否位于 S3/Azure/GCS——
-- 这是所有“文件不存在 / key 不存在”类 finding 的前提。
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
