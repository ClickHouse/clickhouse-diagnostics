# Collecting a whole cluster

`clickhouse-diagnostic cluster` runs the diagnostic on **every host of a self-managed cluster** in one go. It asks one node which hosts the cluster has (`system.clusters`), prints the command it will run for each host, and runs them after you confirm. You get one bundle per host, plus a summary of which ones succeeded. It is part of the `clickhouse-diagnostic` binary, so there is nothing else to install.

## Two ways to run it

| `-collect-from` | What it does | What you get |
|---|---|---|
| `here` (default) | Runs every collection **from this machine**, one host at a time | All system tables of every host, including `text_log` and `query_log` |
| `each-node` | Only **prints** the command to run on each host | Everything, including server log files and configs |

## Limitations

- **Run it from a machine that can reach every host** on the ClickHouse HTTP port (8123, or 8443 with https). One of the nodes or pods is the natural place. The host names come from `system.clusters`, so they must resolve where it runs.
- **`here` mode does not collect server log files or configs from the other hosts.** Those are read from the local disk, so only the node it runs on gets them. It **does** collect `system.text_log` and `system.query_log` from every host, which covers most investigations. When you need the files, use `each-node` and run the printed command on each host.

## Example

On one of the nodes, from the folder holding `clickhouse-diagnostic` and the `queries.*` folders:

```bash
export CLICKHOUSE_PASSWORD='…'        # required: the password is never passed as a flag
./clickhouse-diagnostic cluster -user diag
```

```
Discovered 4 host(s) from localhost (clusters: default, prod_cluster)

  ch-node-1     10.0.0.11   shard 1 replica 1 (asked for the cluster)
  ch-node-2     10.0.0.12   shard 1 replica 2
  ch-node-3     10.0.0.13   shard 2 replica 1
  ch-node-4     10.0.0.14   shard 2 replica 2

Will run, one host at a time:
./clickhouse-diagnostic -mode onprem -protocol http -host localhost -port 8123 -user diag -single-node ...
...
Run 4 collection(s)? [y/N] y
→ ch-node-1 ... ok, 41s, 3.1 MB
...
Bundles in ./cluster_bundles_20261015_101500
```

To get log files and configs from every host, print the per-host commands and run each one on its host:

```bash
./clickhouse-diagnostic cluster -user diag -collect-from each-node
```

## Options

| Option | Meaning |
|---|---|
| `-host` | Node to discover the cluster from. Default `localhost`. |
| `-cluster NAME` | Only the hosts of this cluster. Default: every host `system.clusters` lists, each machine once. |
| `-port`, `-protocol https` | HTTP port and protocol, the same on every host. |
| `-mode gov` | Hashed identifiers. Pass the salt after `--`: `-- -salt <YourPrivateSalt>`. |
| `-- <flags>` | Passed to every run, for example `-- -from 2026-10-01 -to 2026-10-02`. Flags the command sets itself (`-host`, `-password`, `-output-dir`, `-single-node`, …) are refused. |
| `-yes`, `-timeout`, `-output-dir` | Skip the confirmation; time allowed per host (default 30m); where the bundles go. |

## Notes

- Every host needs the user and grants described in the main [README → Required grants](../README.md#required-grants).
- Hosts run one at a time, so one slow node does not compete with the others for the cluster's resources. A host that produces no archive is listed in `summary.txt`, with its `run.log` next to it, and the command exits with code 1.
- Each bundle is uploaded on its own, and each must fit the support portal's 200 MB limit. The summary flags any that do not.
