-- Cloud runs 23.5 and newer, so this root file can use `default`
-- unconditionally (added to system.settings in 23.4) and needs no
-- version rung. The onprem/gov trees keep a 22.8-compatible root plus a
-- 23.4.1.0 override, because their floor is 22.8.
--
-- The effective session-level settings. Config files show what was
-- written on disk; this shows what the server actually resolved.
-- changed = 1 marks every setting that deviates from the default.
--
-- `readonly` is deliberately not collected: it reports this session's
-- readonly=1 (pkg/clickhouse.go:176), so it is 1 for every row on every
-- run and would read as "a profile has locked every setting". Real
-- per-profile constraints live in system.settings_profile_elements.
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.settings
ORDER BY name
