-- 23.4+ variant: system.settings gained `default` in 23.4, which turns
-- changed = 1 from a bare flag into a readable diff (value vs default).
-- The root system.settings.sql is the 22.8-compatible variant.
--
-- Gov: nothing here is hashed, and this file is deliberately identical
-- to the onprem variant — see the root file for why.
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
