-- Root variant for the oldest supported servers (22.8+):
-- `default` and `alias_for` were added to system.settings in 23.4 — see
-- 23.4.1.0/system.settings.sql for the variant that includes `default`.
--
-- Gov: most settings are numeric or boolean tuning knobs and name
-- nothing, so they are kept as-is. A few carry free-form strings that
-- can name a host, URL or filesystem path
-- (format_avro_schema_registry_url is the clearest example), so
-- String-typed values are hashed with the run salt.
SELECT
    name,
    if(type = 'String' AND value != '',
       hex(SHA256(concat(value, '%salt%'))),
       value)                                       AS value,
    changed,
    readonly,
    type
FROM system.settings
ORDER BY name
