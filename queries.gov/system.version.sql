-- tier: essential
-- The server version, and the anchors the alert checks need to read the
-- other files the way a live query would: the server's own clock (now(),
-- collected_at) and timezone — DateTime values in the other files are in
-- that timezone — and the host name messages refer to (hashed in gov). In
-- cloud mode this is the replica that answered, not every replica.
SELECT
  version()      AS version,
  hex(SHA256(concat(hostName(), '%salt%'))) AS hostname,
  timezone()     AS timezone,
  toString(now()) AS collected_at
