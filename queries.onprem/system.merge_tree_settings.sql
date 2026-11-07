-- 服务器级 MergeTree 默认值：config.xml 中 <merge_tree> 块经服务器
-- 解析后的结果，外加所有保持编译期默认值的设置。
-- system.settings 是会话层，system.server_settings 是进程层；这里则是
-- 第三层——每张 MergeTree 表都会继承这些值，除非它自己的
-- SETTINGS 子句（system.tables.engine_full）覆盖了它们。
--
-- 为什么要采集它：决定多表集群行为的那些旋钮就在这里，
-- 在 bundle 里别处都找不到——parts_to_throw_insert /
-- parts_to_delay_insert（TOO_MANY_PARTS 的阈值），以及在
-- SharedMergeTree 上的 shared_merge_tree_parts_load_batch_size（每张表
-- 每轮调度多少次 part 拉取，默认 32）和
-- shared_merge_tree_leader_update_period_seconds（每张表多久跑一次
-- leader 选举，默认 30 s——数万张表会让它在 schedule pool 上
-- 变成每秒数百次选举）。只有知道当前值之后，
-- 建议修改它们的支持意见才是安全的。
--
-- 约 350 行，几十 KiB。`changed = 1` 标记偏离默认值的项。
-- `default` 列只存在于较新的服务器上，因此根文件
-- （22.8 下限）读取 name、value、changed 和 type——这四个列在
-- 22.8 上都存在（已在 22.8.21.38 上验证）。
SELECT
    name,
    value,
    changed,
    type
FROM system.merge_tree_settings
ORDER BY name
