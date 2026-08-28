-- Cloud runs 23.5 and newer, so this root file can use `default`
-- unconditionally (added to system.settings in 23.4) and needs no
-- version rung. The onprem/gov trees keep a 22.8-compatible root plus a
-- 23.4.1.0 override, because their floor is 22.8.
--
-- The effective session-level settings. Config files show what was
-- written on disk; this shows what the server actually resolved.
-- changed = 1 marks every setting that deviates from the default.
SELECT
    name,
    value,
    `default`,
    changed,
    readonly,
    type
FROM system.settings
ORDER BY name
