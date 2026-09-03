-- Root variant for the oldest supported servers (22.8+):
-- `default` and `alias_for` were added to system.settings in 23.4 — see
-- 23.4.1.0/system.settings.sql for the variant that includes `default`.
--
-- The effective session-level settings. Config files show what was
-- written on disk; this shows what the server actually resolved.
-- changed = 1 marks every setting that deviates from this version's
-- default, which is the fastest read of "what did they tune".
--
-- `readonly` is deliberately not collected. Every query this tool sends
-- carries readonly=1 (pkg/clickhouse.go:176), and that session setting
-- is what system.settings.readonly reports — so the column comes back 1
-- for all ~1700 rows on every run, in every mode, no matter how the
-- customer's profiles are configured. Verified against 26.7: readonly=1
-- session → 1 for 1717/1717 rows, normal session → 0 for 1717/1717.
-- Shipping it would not be merely uninformative, it would read as "a
-- profile has locked every setting". Real per-profile constraints live
-- in system.settings_profile_elements, which is where a collector for
-- them belongs.
SELECT
    name,
    value,
    changed,
    type
FROM system.settings
ORDER BY name
