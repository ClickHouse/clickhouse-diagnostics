-- tier: essential
-- 面向最旧受支持服务器（22.8+）的根目录变体：
-- `default` 和 `alias_for` 于 23.4 加入 system.settings ——
-- 包含 `default` 的变体见 23.4.1.0/system.settings.sql。
--
-- 生效的会话级设置。配置文件显示磁盘上写了什么；
-- 这里显示服务器实际解析出的结果。
-- changed = 1 标记每个偏离本版本默认值的设置，
-- 这是“他们调了什么”最快的读法。
--
-- Gov：这里没有任何东西被哈希，本文件有意与 onprem
-- 变体保持一致。设置值是 ClickHouse 调优旋钮，
-- 不是客户标识符 —— 哈希它比无用还糟，
-- 因为 gov 名称映射（internal/gov_mapping.go）由
-- system.tables 构建，只覆盖数据库和表名。
-- 支持人员会收到无法解码的十六进制，取代
-- 这个采集器存在所要展示的唯一内容。不要“恢复”哈希。
--
-- 有意不采集 `readonly`：它报告的是本会话的
-- readonly=1（pkg/clickhouse.go:176），所以每次运行时每一行都是 1，
-- 读起来会像“某个 profile 锁定了所有设置”。真正的
-- 按 profile 约束位于 system.settings_profile_elements。
SELECT
    name,
    value,
    changed,
    type
FROM system.settings
ORDER BY name
