-- system.server_settings first appeared in 23.3, so this collector
-- lives only in a version directory and has no root counterpart: on an
-- older server the finder skips it instead of failing the run. It is
-- gated at 23.4.1.0 to reuse the version directory this tree already
-- has rather than adding one for a two-release gap.
--
-- Gov: this table names more than system.settings does — path,
-- tmp_path, interserver_http_host and friends. Unlike the config files
-- (which gov withholds outright, because remote_servers and zookeeper
-- topology cannot be selectively hashed), this is a flat name/value
-- table: the exposure is confined to String-typed values, which hash
-- cleanly with the run salt. Setting *names* and the numeric tuning
-- values stay readable, which is the part we need.
SELECT
    name,
    if(type = 'String' AND value != '',
       hex(SHA256(concat(value, '%salt%'))),
       value)                                       AS value,
    if(type = 'String' AND `default` != '',
       hex(SHA256(concat(`default`, '%salt%'))),
       `default`)                                   AS `default`,
    changed,
    type
FROM system.server_settings
ORDER BY name
