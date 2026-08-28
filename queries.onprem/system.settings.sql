-- Root variant for the oldest supported servers (22.8+):
-- `default` and `alias_for` were added to system.settings in 23.4 — see
-- 23.4.1.0/system.settings.sql for the variant that includes `default`.
--
-- The effective session-level settings. Config files show what was
-- written on disk; this shows what the server actually resolved.
-- changed = 1 marks every setting that deviates from this version's
-- default, which is the fastest read of "what did they tune".
SELECT
    name,
    value,
    changed,
    readonly,
    type
FROM system.settings
ORDER BY name
