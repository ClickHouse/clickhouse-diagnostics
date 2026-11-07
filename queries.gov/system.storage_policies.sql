-- tier: essential
-- Gov：policy、volume 和磁盘名会被哈希（与 system.disks.name 相同的盐）。
SELECT
    hex(SHA256(concat(policy_name, '%salt%')))                    AS policy_name,
    hex(SHA256(concat(volume_name, '%salt%')))                    AS volume_name,
    volume_priority,
    arrayMap(d -> hex(SHA256(concat(d, '%salt%'))), disks)        AS disks,
    max_data_part_size,
    move_factor,
    prefer_not_to_merge
FROM system.storage_policies
ORDER BY policy_name, volume_priority
