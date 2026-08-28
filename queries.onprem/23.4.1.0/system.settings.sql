-- 23.4+ variant: system.settings gained `default` in 23.4, which turns
-- changed = 1 from a bare flag into a readable diff (value vs default).
-- The root system.settings.sql is the 22.8-compatible variant.
SELECT
    name,
    value,
    `default`,
    changed,
    readonly,
    type
FROM system.settings
ORDER BY name
