-- Server-wide MergeTree defaults — see queries.onprem/system.merge_tree_settings.sql
-- for why the file exists (TOO_MANY_PARTS thresholds, SharedMergeTree
-- parts_load_batch_size / leader_update_period). ~350 rows.
--
-- Gov: setting NAMES are ClickHouse constants and stay readable, as in
-- system.settings / system.server_settings. VALUES are numbers, booleans or
-- codecs for all but a handful of String settings, and those few are handled
-- by what they contain rather than hashed wholesale (a hashed value has no
-- decoder — the gov mapping CSV covers database and table names only):
--   * storage_policy and disk are customer-chosen names that system.disks and
--     system.storage_policies already hash with the same salt, so they are
--     hashed here too and the three files still join;
--   * anything path- or URL-shaped (remote_fs_zero_copy_zookeeper_path), the
--     workload names, and the settings whose value is a list of the
--     customer's column or index names are REMOVED — the sentinel the config
--     sanitizer uses — so the archive shows THAT they were customised and
--     not what to. An empty value stays empty: there is nothing to remove,
--     and 'REMOVED' would read as "something was configured here".
-- Verified on 22.8.21.38 and 26.7: name, value, changed, type exist on both;
-- the String-typed settings on 26.7 are exactly the ones named here plus
-- the compression codecs, which are not identifying.
SELECT
    name,
    multiIf(value = '',                                        value,
            name IN ('storage_policy', 'disk'),                hex(SHA256(concat(value, '%salt%'))),
            value LIKE '/%' OR value LIKE '%://%'
              OR name IN ('merge_workload', 'mutation_workload',
                          'columns_to_prewarm_mark_cache',
                          'exclude_materialize_skip_indexes_on_merge',
                          'cache_populated_by_fetch_filename_regexp'), 'REMOVED',
                                                               value) AS value,
    changed,
    type
FROM system.merge_tree_settings
ORDER BY name
