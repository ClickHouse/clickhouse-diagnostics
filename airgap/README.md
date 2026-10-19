# Air-gapped dashboards

By default `dashboard.html` loads its chart library (Chart.js) from a public CDN. When the page is opened on a machine with **no internet access**, the library never arrives, and the page stays almost empty: not just the charts, but also the alerts and tables.

Add **`-airgap`** and the diagnostic embeds Chart.js, which ships inside the binary, directly in `dashboard.html`. The page then opens anywhere, with no network access at all. Nothing else changes: the same queries run, and the same bundle is written. Without the flag, every run behaves exactly as before.

## When to use it

Use it when the dashboard will be **opened** on a machine without internet: an air-gapped network, a locked-down workstation, or a browser that blocks CDNs. What matters is where the file is opened, not where the diagnostic runs.

## Examples

One node, from the folder holding `clickhouse-diagnostic` and the `queries.*` folders:

```bash
export CLICKHOUSE_PASSWORD='…'
./clickhouse-diagnostic -host localhost -port 8123 -user diag -airgap
```

Collecting a whole cluster ([`cluster-collect/`](../cluster-collect/README.md)): pass it after `--` so every host's run gets it:

```bash
export CLICKHOUSE_PASSWORD='…'
./clickhouse-diagnostic cluster -user diag -- -airgap
```

Combined with other flags, as usual:

```bash
./clickhouse-diagnostic -host ch-node-1 -user diag -from 2026-10-01 -to 2026-10-02 -airgap
```

To check the result, open `dashboard.html` and look at the footer: it reads **"Chart.js 4.4.3 embedded (air-gap build, no network needed)"** instead of "requires Chart.js CDN".

## Good to know

- **Size:** `dashboard.html` grows by about 200 KB (about 70 KB in the `.tar.gz`).
- **Gov mode** (`-mode gov`) produces no dashboard, so the flag has no effect there.
- **`schema_graph.html`** needs no network in any mode.
- **Licence:** Chart.js is MIT-licensed. Its copyright notice stays at the top of the embedded script, and the full licence is in [`internal/dashboard/assets/LICENSE.chartjs.md`](../internal/dashboard/assets/LICENSE.chartjs.md).
