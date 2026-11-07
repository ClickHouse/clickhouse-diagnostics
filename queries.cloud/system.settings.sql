-- tier: essential
-- Cloud 运行 23.5 及更新版本，因此这个根文件可以无条件使用
-- `default`（23.4 加入 system.settings），不需要
-- 版本层级。onprem/gov 目录保留兼容 22.8 的根文件加上
-- 23.4.1.0 覆盖，因为它们的下限是 22.8。
--
-- 生效的会话级设置。配置文件显示磁盘上写入了什么；
-- 本文件显示服务器实际解析出的结果。
-- changed = 1 标记每个偏离默认值的设置。
--
-- 故意不采集 `readonly`：它报告本次会话的
-- readonly=1（pkg/clickhouse.go:176），因此每次运行的每一行都是 1，
-- 会被读成“某个 profile 锁定了所有设置”。真正的
-- 按 profile 约束在 system.settings_profile_elements 中。
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.settings
ORDER BY name
