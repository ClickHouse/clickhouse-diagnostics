-- 服务器范围的 MergeTree 默认值：config.xml 中 <merge_tree> 块在服务器
-- 解析后的结果，以及所有仍保持编译时默认值的设置。
-- system.settings 是会话，system.server_settings 是进程；本文件是
-- 第三层——每张 MergeTree 表继承的内容，除非它自己的
-- SETTINGS 子句（system.tables.engine_full）覆盖。
--
-- 为什么采集：决定多表集群行为的旋钮
-- 就在这里，bundle 中别处没有——parts_to_throw_insert /
-- parts_to_delay_insert（TOO_MANY_PARTS 阈值），以及在
-- SharedMergeTree 上的 shared_merge_tree_parts_load_batch_size（每轮一张表
-- 调度多少个 part fetch，默认 32）和
-- shared_merge_tree_leader_update_period_seconds（每张表多久运行
-- 一次 leader election，默认 30 秒——数万张表会把
-- 它变成 schedule pool 上每秒数百次 election）。只有在
-- 知道当前值之后，建议修改它们才是安全的。
--
-- 从一个副本读取，和 system.settings、system.server_settings 一样：
-- <merge_tree> 块在整个集群部署，fan-out 会按副本
-- 重复约 350 行相同数据。约 350 行，几十 KiB。
-- `changed = 1` 标记偏离默认值的项。与
-- onprem/gov 根文件相同的四列，使三个文件读起来一致。
SELECT
    name,
    value,
    changed,
    type
FROM system.merge_tree_settings
ORDER BY name
