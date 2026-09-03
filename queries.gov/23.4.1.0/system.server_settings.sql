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
-- Gov: nothing here is hashed — not the setting names, not the values.
-- Names are ClickHouse constants, and a hashed value would have no
-- decoder anywhere, because the gov name mapping
-- (internal/gov_mapping.go) is built from system.tables and so covers
-- database and table names only. Support would receive 64 hex
-- characters and no way back.
--
-- The handful of values that name customer infrastructure are REMOVED
-- instead, which is both safer and far easier to reason about than
-- salting them: a removed value cannot leak and needs no key to read.
-- 'REMOVED' is the same sentinel the config sanitizer uses for redacted
-- credentials (internal/collection/heuristics.go). The setting name,
-- `default` and `changed` all survive, so the archive still shows THAT
-- a path was customised without showing what it is — which is the part
-- a support engineer actually needs.
--
-- Keyed on the VALUE's shape rather than on a list of setting names,
-- because the names follow no convention: `config-file`,
-- `include_from`, `logger.log` and `logger.errorlog` are all
-- filesystem paths with no "path" in the name, so a name-based
-- allowlist drops precisely the settings nobody thinks of. Any
-- absolute path or URL goes, plus the customer-chosen names that are
-- neither. There is no `type = 'String'` guard because none is needed:
-- checked on 26.7, every row the shape test matches is already String
-- (17/17), so leaving the guard off also covers a URI-typed setting
-- without ever matching a number.
--
-- `default` is ClickHouse's own compiled-in default, not the
-- customer's, so it stays readable — that is what makes `changed = 1`
-- with a REMOVED value legible at all.
--
-- An empty value stays empty rather than becoming REMOVED. There is
-- nothing to remove, and 'REMOVED' would imply a path was configured
-- where none is: interserver_http_host is unset on a stock server and
-- reading "not configured" is the useful, harmless answer.
SELECT
    name,
    if(identifying AND value != '', 'REMOVED', value) AS value,
    `default`,
    changed,
    type
FROM (
    SELECT
        name,
        value,
        `default`,
        changed,
        type,
        value LIKE '/%' OR value LIKE '%://%'
        OR `default` LIKE '/%' OR `default` LIKE '%://%'
        OR name IN ('default_database', 'default_replica_name',
                    'interserver_http_host', 'default_profile',
                    'merge_workload', 'mutation_workload')
                                                    AS identifying
    FROM system.server_settings
)
ORDER BY name
