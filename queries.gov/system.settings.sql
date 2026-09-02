-- Root variant for the oldest supported servers (22.8+):
-- `default` and `alias_for` were added to system.settings in 23.4 — see
-- 23.4.1.0/system.settings.sql for the variant that includes `default`.
--
-- The effective session-level settings. Config files show what was
-- written on disk; this shows what the server actually resolved.
-- changed = 1 marks every setting that deviates from this version's
-- default, which is the fastest read of "what did they tune".
--
-- Gov: nothing here is hashed, and this file is deliberately identical
-- to the onprem variant. A setting value is a ClickHouse tuning knob,
-- not a customer identifier — and hashing one would be worse than
-- useless, because the gov name mapping (internal/gov_mapping.go) is
-- built from system.tables and covers database and table names only.
-- Support would receive hex it has no way to decode, in place of the
-- one thing this collector exists to show. Do not "restore" the hash.
--
-- `readonly` is deliberately not collected: it reports this session's
-- readonly=1 (pkg/clickhouse.go:176), so it is 1 for every row on every
-- run and would read as "a profile has locked every setting". Real
-- per-profile constraints live in system.settings_profile_elements.
SELECT
    name,
    value,
    changed,
    type
FROM system.settings
ORDER BY name
