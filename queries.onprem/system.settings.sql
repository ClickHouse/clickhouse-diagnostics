-- tier: essential
-- 面向最旧受支持服务器（22.8+）的根变体：
-- `default` 和 `alias_for` 是在 23.4 加入 system.settings 的——包含
-- `default` 的变体见 23.4.1.0/system.settings.sql。
--
-- 会话级生效设置。配置文件显示的是磁盘上写了什么，
-- 这里显示的是服务器实际解析出的结果。
-- changed = 1 标记每个偏离当前版本默认值的设置，
-- 这是回答“客户调了哪些参数”最快的方式。
--
-- `readonly` 被刻意不采集。本工具发出的每条查询
-- 都带有 readonly=1（pkg/clickhouse.go:176），而 system.settings.readonly
-- 报告的正是这个会话设置——所以无论客户 profile 如何配置，
-- 该列在每次运行、每种模式下都会对全部约 1700 行
-- 返回 1。已对照 26.7 验证：readonly=1 的
-- 会话 → 1717/1717 行均为 1，普通会话 → 1717/1717 行均为 0。
-- 把它放进诊断包不仅毫无信息量，还会被读成“某个
-- profile 锁死了所有设置”。真正的按 profile 约束存放在
-- system.settings_profile_elements，为它们写 collector 也应该
-- 放在那里。
SELECT
    name,
    value,
    changed,
    type
FROM system.settings
ORDER BY name
