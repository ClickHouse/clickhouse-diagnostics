-- system.server_settings first appeared in 23.3, so this collector
-- lives only in a version directory and has no root counterpart: on an
-- older server the finder skips it instead of failing the run. It is
-- gated at 23.4.1.0 to reuse the version directory this tree already
-- has rather than adding one for a two-release gap.
--
-- Server-level settings — max_server_memory_usage, the background
-- pools, the paths. system.settings covers the session; this covers
-- the process, and the two answer different questions.
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.server_settings
ORDER BY name
