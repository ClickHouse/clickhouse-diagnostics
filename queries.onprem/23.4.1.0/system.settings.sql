-- tier: essential
-- 23.4+ 变体：system.settings 在 23.4 增加了 `default`，使
-- changed = 1 从一个裸标志变成可读的差异（value 对比 default）。
-- 根文件 system.settings.sql 是与 22.8 兼容的变体。
--
-- `readonly` 被刻意不采集：它报告的是本会话的
-- readonly=1（pkg/clickhouse.go:176），所以每次运行每一行都是 1，
-- 会被读成“某个 profile 锁死了所有设置”。真正的
-- 按 profile 约束存放在 system.settings_profile_elements。
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.settings
ORDER BY name
