package main

import (
	"fmt"
	"strconv"
	"strings"

	"clickhouse-diagnostic/pkg"
)

// nodeIdentity is what the bundle says about the server it describes. Two
// bundles in one escalation were read against the wrong node — one came
// through a load balancer (system tables from replica A, log files from
// replica B), the other from a sibling site — and both were spotted only by
// hand from thread ids and uptime. The identity probe puts the answer in
// the execution log, the dashboard header and the pre-pass coverage line.
type nodeIdentity struct {
	Host          string   // hostName() — the node the system tables describe
	FQDN          string   // FQDN()
	UptimeSeconds int64    // uptime() — how long ago this node (re)started
	Samples       []string // hostName() from repeated probes; differing values mean a load balancer
	Err           error    // probe failure; identity unknown, warnings suppressed
}

// probeNodeIdentity asks the server who it is. Three separate round trips for
// hostName() on purpose: through a load balancer they land on different
// replicas, and that is the only cheap way to see one from the client side.
func probeNodeIdentity(client *pkg.ClickHouseClient) nodeIdentity {
	var id nodeIdentity
	raw, err := client.ExecuteQuery("SELECT hostName(), FQDN(), toString(uptime()) FORMAT TSV")
	if err != nil {
		id.Err = err
		return id
	}
	f := strings.Split(strings.TrimSpace(raw), "\t")
	if len(f) >= 3 {
		id.Host, id.FQDN = f[0], f[1]
		id.UptimeSeconds, _ = strconv.ParseInt(f[2], 10, 64)
	}
	id.Samples = append(id.Samples, id.Host)
	for i := 0; i < 2; i++ {
		if s, err := client.ExecuteQuery("SELECT hostName() FORMAT TSV"); err == nil {
			id.Samples = append(id.Samples, strings.TrimSpace(s))
		}
	}
	return id
}

// loadBalancerSuspected reports whether repeated hostName() probes answered
// with more than one name.
func loadBalancerSuspected(samples []string) bool {
	seen := map[string]bool{}
	for _, s := range samples {
		if s = strings.TrimSpace(s); s != "" {
			seen[s] = true
		}
	}
	return len(seen) > 1
}

// sameMachine says whether the server's reported name and the collector's
// own hostname plausibly denote one machine: equal, or equal on the first
// DNS label (a server reports "ch-01" while the OS says "ch-01.example.internal",
// or the reverse).
func sameMachine(serverHost, serverFQDN, localHostname string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	first := func(s string) string { return strings.SplitN(norm(s), ".", 2)[0] }
	l := norm(localHostname)
	if l == "" {
		return true // cannot tell; do not warn
	}
	for _, s := range []string{serverHost, serverFQDN} {
		if s = norm(s); s != "" && (s == l || first(s) == first(l)) {
			return true
		}
	}
	return false
}

// isLoopback reports whether the -host flag points at this machine.
func isLoopback(target string) bool {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0":
		return true
	}
	return false
}

// nodeWarnings turns the identity probe into operator warnings. Each names
// the consequence for the bundle, not just the fact.
func nodeWarnings(id nodeIdentity, target, localHostname string, collectingLocalFiles bool) []string {
	if id.Err != nil {
		return nil
	}
	var out []string
	if loadBalancerSuspected(id.Samples) {
		out = append(out, fmt.Sprintf("Warning: -host %s answered as %s — it is a load balancer or a DNS round-robin. "+
			"Every system table in this bundle will come from whichever replica answers each query, so the bundle "+
			"describes no single node. Point -host at one replica (or run the tool on it).",
			target, strings.Join(uniqueStrings(id.Samples), " and ")))
	}
	if collectingLocalFiles && !isLoopback(target) && !sameMachine(id.Host, id.FQDN, localHostname) {
		out = append(out, fmt.Sprintf("Warning: the system tables describe %s but host facts, configuration and log files "+
			"are read from THIS machine (%s). Run the tool on %s, or pass -host-info off -logs off -skip-config "+
			"so the bundle does not mix two hosts.", id.Host, localHostname, id.Host))
	}
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// humanUptime renders uptime() for the execution log and the coverage line.
func humanUptime(seconds int64) string {
	if seconds <= 0 {
		return "unknown"
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// isSharedMergeTree reads the raw value of `SELECT value FROM system.settings
// WHERE name = 'cloud_mode'`. Cloud mode is what a self-hosted SharedMergeTree
// stack runs with; every replica then keeps its own system tables.
func isSharedMergeTree(cloudModeValue string) bool {
	v := strings.ToLower(strings.TrimSpace(cloudModeValue))
	return v == "1" || v == "true"
}

// smtCollectionDecision is the outcome of finding a SharedMergeTree cluster
// under -mode onprem.
type smtCollectionDecision struct {
	EffectiveMode string // "cloud" when the run switches, else "onprem"
	Note          string // one line for the execution log's `collection:` key
	Message       string // what the operator sees
}

// decideSMTCollection: -mode onprem on a SharedMergeTree cluster collects one
// node of N, which in one sev-1 meant five bundles that described nothing
// useful. The run therefore switches to cloud collection — every system
// table fans out over the `default` cluster — while still collecting this
// node's host facts, configuration and log files, which cloud mode alone
// does not. fanoutErr is the result of probing
// clusterAllReplicas(default, system.one); a refused probe (no REMOTE grant,
// no `default` cluster) means the switch cannot happen and the run stays on
// one node, saying so. singleNode is the -single-node flag.
func decideSMTCollection(fanoutErr error, singleNode bool, hint string) smtCollectionDecision {
	switch {
	case singleNode:
		return smtCollectionDecision{
			EffectiveMode: "onprem",
			Note:          "onprem, single node by request (-single-node) on a SharedMergeTree cluster — the other replicas' system tables are not in this bundle",
			Message:       hint,
		}
	case fanoutErr != nil:
		return smtCollectionDecision{
			EffectiveMode: "onprem",
			Note: "onprem, single node: SharedMergeTree detected (cloud_mode = 1) but the cluster fan-out probe failed — " +
				firstLine(fanoutErr.Error()),
			Message: "Warning: this server runs SharedMergeTree (cloud_mode = 1), but clusterAllReplicas(default, …) failed: " +
				firstLine(fanoutErr.Error()) + "\n    The bundle will describe THIS node only. To cover every replica grant " +
				"REMOTE and CREATE TEMPORARY TABLE ON *.* to the diagnostic user and re-run.",
		}
	default:
		return smtCollectionDecision{
			EffectiveMode: "cloud",
			Note: "onprem → cloud: SharedMergeTree detected (cloud_mode = 1). Every system table fans out over the " +
				"default cluster (clusterAllReplicas); host facts, configuration and log files are this node's",
			Message: "SharedMergeTree cluster detected (cloud_mode = 1): switching to cloud collection so the system tables " +
				"cover every replica, not just this node. Host facts, configuration and log files stay local to this node. " +
				"Pass -single-node to collect this node only.",
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
