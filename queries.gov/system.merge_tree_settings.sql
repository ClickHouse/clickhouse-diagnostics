-- 服务器级 MergeTree 默认值 —— 该文件为何存在见
-- queries.onprem/system.merge_tree_settings.sql（TOO_MANY_PARTS 阈值、
-- SharedMergeTree 的 parts_load_batch_size / leader_update_period）。约 350 行。
--
-- Gov：设置名是 ClickHouse 常量，保持可读，与
-- system.settings / system.server_settings 一样。除少数 String 设置外，
-- 值都是数字、布尔值或 codec，而那少数几个按它们包含的内容处理，
-- 而不是整体哈希（被哈希的值没有解码器 —— gov 映射 CSV
-- 只覆盖数据库和表名）：
--   * storage_policy 和 disk 是客户自选名称，system.disks 和
--     system.storage_policies 已经用相同的盐哈希过它们，所以这里
--     也做哈希，三个文件仍可关联；
--   * 任何路径或 URL 形状的值（remote_fs_zero_copy_zookeeper_path）、
--     workload 名称，以及值为客户列名或索引名列表的设置，
--     都会被替换为 REMOVED —— 配置脱敏器使用的哨兵值 ——
--     这样归档表明它们被定制过，却不能说明改成了什么。
--     空值保持为空：没有东西可移除，
--     而 'REMOVED' 会读作“这里配置过东西”。
-- 已在 22.8.21.38 和 26.7 上验证：name、value、changed、type 两者都有；
-- 26.7 上的 String 类型设置恰好就是这里点名的那些，再加上
-- 压缩 codec，后者不具备标识性。
SELECT
    name,
    multiIf(value = '',                                        value,
            name IN ('storage_policy', 'disk'),                hex(SHA256(concat(value, '%salt%'))),
            value LIKE '/%' OR value LIKE '%://%'
              OR name IN ('merge_workload', 'mutation_workload',
                          'columns_to_prewarm_mark_cache',
                          'exclude_materialize_skip_indexes_on_merge',
                          'cache_populated_by_fetch_filename_regexp'), 'REMOVED',
                                                               value) AS value,
    changed,
    type
FROM system.merge_tree_settings
ORDER BY name
