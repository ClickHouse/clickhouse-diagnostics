# Troubleshooting

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## Troubleshooting

**"Queries folder './queries.<mode>' does not exist"**
The mode you passed has no matching directory at the repo root. Check `-mode` matches one of `cloud`, `onprem`, `gov`.

**"Config directory does not exist"**
Use `-skip-config` or pass the correct path with `-config-dir`. Cloud users should always use `-skip-config`.

**"Connection refused" / timeouts**
Verify the server is reachable on the chosen `-protocol` + `-host` + `-port`. The default port differs by protocol (8123 vs 8443).

**"security: query must be SELECT or WITH" (alert blocked)**
An alert YAML has a non-read-only statement. Alert queries are restricted to `SELECT` / `WITH`; rewrite the query or remove the rule.

**"Invalid gov-mode salt: must be 8–64 ASCII alphanumeric characters"**
The `-salt` value (or interactive prompt input) contains spaces, punctuation, or is the wrong length. Pick a value that matches `[A-Za-z0-9]{8,64}` — see [Gov mode and hashed names](modes.md#gov-mode-and-hashed-names).

**Dashboard charts blank**
The generated `dashboard.html` loads Chart.js from a public CDN. Open it in an environment with internet access, or see [`airgap/`](../airgap/README.md) to build a dashboard that needs no network.

**"Permission denied" on config files**
You don't have read access to `-config-dir`. Either run with appropriate privileges or use `-skip-config`.
