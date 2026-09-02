-- system.server_settings first appeared in 23.3, so this collector
-- lives only in a version directory and has no root counterpart: on an
-- older server the finder skips it instead of failing the run. It is
-- gated at 23.4.1.0 to reuse the version directory this tree already
-- has rather than adding one for a two-release gap.
--
-- Server-level settings — max_server_memory_usage, the background
-- pools, the paths. system.settings covers the session; this covers
-- the process, and the two answer different questions.
--
-- Gov: setting names and the numeric tuning values are ClickHouse
-- constants with nothing to protect, so they stay readable — that is
-- the whole point of the collector. What does need protecting is the
-- handful of String values that name customer infrastructure, which is
-- the same exposure gov withholds configuration/ and host_info.json
-- for. On 26.7 the predicate below hashes 22 of 439 rows and leaves
-- the other 417 readable.
--
-- Keyed on the VALUE's shape rather than on a list of setting names,
-- because the names follow no convention: `config-file`,
-- `include_from`, `logger.log` and `logger.errorlog` are all
-- filesystem paths with no "path" in the name, so a name-based
-- allowlist leaks precisely the settings nobody thought of. Any
-- absolute path or URL is hashed; the customer-chosen names that are
-- neither are listed explicitly.
--
-- The flag is computed in a subquery (the shape system.disks already
-- uses here) because an outer `if(… value …) AS value` that also reads
-- `default` reads as a mutual alias reference — verified to fail with
-- CYCLIC_ALIASES on 23.7. It is decided once for the row so a hashed
-- value still compares equal to its hashed default when the setting is
-- untouched, and so a path default cannot be hashed while an
-- overridden path value is emitted raw.
--
-- `default_database` hashes to exactly what the gov name mapping CSV
-- carries for that database (internal/gov_mapping.go uses the same
-- concat+SHA256+salt), so support can decode that one.
SELECT
    name,
    if(identifying AND value != '',
       hex(SHA256(concat(value, '%salt%'))),
       value)                                       AS value,
    if(identifying AND `default` != '',
       hex(SHA256(concat(`default`, '%salt%'))),
       `default`)                                   AS `default`,
    changed,
    type
FROM (
    SELECT
        name,
        value,
        `default`,
        changed,
        type,
        type = 'String'
        AND (value LIKE '/%' OR value LIKE '%://%'
             OR `default` LIKE '/%' OR `default` LIKE '%://%'
             OR name IN ('default_database', 'default_replica_name',
                         'interserver_http_host', 'default_profile',
                         'merge_workload', 'mutation_workload'))
                                                    AS identifying
    FROM system.server_settings
)
ORDER BY name
