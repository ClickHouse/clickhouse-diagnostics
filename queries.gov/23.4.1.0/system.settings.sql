-- tier: essential
-- 23.4+ 变体：system.settings 在 23.4 获得 `default`，这让
-- changed = 1 从单纯标志变成可读的差异（value 对 default）。
-- 根目录 system.settings.sql 是兼容 22.8 的变体。
--
-- Gov：这里没有任何东西被哈希，本文件有意与 onprem
-- 变体保持一致 —— 原因见根目录文件。
--
-- 有意不采集 `readonly`：它报告的是本会话的
-- readonly=1（pkg/clickhouse.go:176），所以每次运行时每一行都是 1，
-- 读起来会像“某个 profile 锁定了所有设置”。真正的
-- 按 profile 约束位于 system.settings_profile_elements。
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.settings
ORDER BY name
