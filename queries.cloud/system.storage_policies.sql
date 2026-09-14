-- Which policies exist and which disks back them. Together with system.disks.type
-- (ObjectStorage vs Local) this says whether table data lives on S3/Azure/GCS —
-- the precondition for every "file doesn't exist / key does not exist" finding.
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
