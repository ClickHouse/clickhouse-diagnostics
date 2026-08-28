-- 23.4+ variant: system.settings gained `default` in 23.4, which turns
-- changed = 1 from a bare flag into a readable diff (value vs default).
-- The root system.settings.sql is the 22.8-compatible variant.
--
-- Gov: String-typed values are hashed with the run salt — see the root
-- file for why. `default` is hashed on the same rule, so a hashed value
-- and its hashed default still compare equal when unchanged.
SELECT
    name,
    if(type = 'String' AND value != '',
       hex(SHA256(concat(value, '%salt%'))),
       value)                                       AS value,
    if(type = 'String' AND `default` != '',
       hex(SHA256(concat(`default`, '%salt%'))),
       `default`)                                   AS `default`,
    changed,
    readonly,
    type
FROM system.settings
ORDER BY name
