package main

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadBalancerSuspected(t *testing.T) {
	if loadBalancerSuspected([]string{"ch-01", "ch-01", "ch-01"}) {
		t.Error("one name three times is one node")
	}
	if !loadBalancerSuspected([]string{"ch-09", "ch-20", "ch-09"}) {
		t.Error("two names is a load balancer")
	}
	if loadBalancerSuspected([]string{"", "ch-01"}) {
		t.Error("an empty sample (failed probe) must not read as a second node")
	}
}

func TestSameMachine(t *testing.T) {
	cases := []struct {
		host, fqdn, local string
		want              bool
	}{
		{"ch-01", "ch-01.example.internal", "ch-01.example.internal", true},
		{"ch-01", "ch-01.example.internal", "ch-01", true},
		{"ch-09", "ch-09.vm.example", "laptop.local", false},
		{"ch-01", "", "", true}, // unknown local name: cannot tell, do not warn
		// Two qualified names that share a first label are two machines.
		{"ch-01", "ch-01.site-a.example", "ch-01.site-b.example", false},
		{"ch-01.site-a.example", "ch-01.site-a.example", "ch-01.site-a.example", true},
	}
	for _, c := range cases {
		if got := sameMachine(c.host, c.fqdn, c.local); got != c.want {
			t.Errorf("sameMachine(%q,%q,%q) = %v, want %v", c.host, c.fqdn, c.local, got, c.want)
		}
	}
}

// The two mix-ups from the escalation this guards against: a load balancer
// answering as several replicas, and local files collected beside another
// node's system tables.
func TestNodeWarnings(t *testing.T) {
	lb := nodeIdentity{Host: "ch-09", FQDN: "ch-09.x", Samples: []string{"ch-09", "ch-20", "ch-09"}}
	w := nodeWarnings(lb, "clickhouse-lb.example", "ch-09.x", "onprem", false)
	if len(w) != 1 || !strings.Contains(w[0], "load balancer") || !strings.Contains(w[0], "ch-09 and ch-20") {
		t.Errorf("load balancer warning missing: %v", w)
	}

	remote := nodeIdentity{Host: "ch-03", FQDN: "ch-03.x", Samples: []string{"ch-03", "ch-03", "ch-03"}}
	w = nodeWarnings(remote, "ch-03.x", "ops-laptop", "onprem", true)
	if len(w) != 1 || !strings.Contains(w[0], "read from THIS machine (ops-laptop)") {
		t.Errorf("remote-host warning missing: %v", w)
	}
	// Same machine, or loopback, or not collecting local files: quiet.
	if w := nodeWarnings(remote, "ch-03.x", "ch-03", "onprem", true); len(w) != 0 {
		t.Errorf("same machine must not warn: %v", w)
	}
	if w := nodeWarnings(remote, "localhost", "ops-laptop", "onprem", true); len(w) != 0 {
		t.Errorf("loopback must not warn: %v", w)
	}
	if w := nodeWarnings(remote, "ch-03.x", "ops-laptop", "onprem", false); len(w) != 0 {
		t.Errorf("no local files collected, nothing to mix: %v", w)
	}
	if w := nodeWarnings(nodeIdentity{Err: errors.New("no grant")}, "x", "y", "onprem", true); len(w) != 0 {
		t.Errorf("a failed probe must stay silent: %v", w)
	}
}

func TestDecideSMTCollection(t *testing.T) {
	d := decideSMTCollection(nil, false, "hint")
	if d.EffectiveMode != "cloud" || !strings.Contains(d.Note, "onprem → cloud") || !strings.Contains(d.Message, "switching to cloud collection") {
		t.Errorf("fan-out available must switch: %+v", d)
	}
	d = decideSMTCollection(errors.New("Code: 497. DB::Exception: user: Not enough privileges. To execute this query, it's necessary to have the grant REMOTE ON *.*"), false, "hint")
	if d.EffectiveMode != "onprem" || !strings.Contains(d.Note, "fan-out probe failed") || !strings.Contains(d.Message, "REMOTE") {
		t.Errorf("failed fan-out must stay on one node and say why: %+v", d)
	}
	d = decideSMTCollection(nil, true, "the hint")
	if d.EffectiveMode != "onprem" || !strings.Contains(d.Note, "-single-node") || d.Message != "the hint" {
		t.Errorf("-single-node must win: %+v", d)
	}
}

func TestIsSharedMergeTreeAndUptime(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true\n": true, "0": false, "": false, "false": false} {
		if isSharedMergeTree(v) != want {
			t.Errorf("isSharedMergeTree(%q) = %v", v, !want)
		}
	}
	for s, want := range map[int64]string{0: "unknown", 59: "0m", 3700: "1h 1m", 90000: "1d 1h", 410798: "4d 18h"} {
		if got := humanUptime(s); got != want {
			t.Errorf("humanUptime(%d) = %q, want %q", s, got, want)
		}
	}
}

// A load-balanced endpoint is how every ClickHouse Cloud service answers, so
// in cloud mode it is a note about where the shared tables came from, not a
// warning that the bundle cannot be trusted. On a self-managed cluster the
// same symptom is the mix-up that made an escalation read the wrong node for a
// day, so there it stays a warning.
func TestNodeWarnings_LoadBalancerReadsByMode(t *testing.T) {
	lb := nodeIdentity{Host: "replica-a", FQDN: "replica-a.svc", Samples: []string{"replica-a", "replica-b", "replica-a"}}

	cloud := nodeWarnings(lb, "svc.eu-central-1.aws.clickhouse.cloud", "laptop.local", "cloud", false)
	if len(cloud) != 1 {
		t.Fatalf("cloud: want one line, got %v", cloud)
	}
	if !strings.HasPrefix(cloud[0], "Note:") {
		t.Errorf("cloud: a load-balanced endpoint must read as a note, got %q", cloud[0])
	}
	for _, unwanted := range []string{"Point -host at one replica", "describes no single node"} {
		if strings.Contains(cloud[0], unwanted) {
			t.Errorf("cloud: advice that does not apply to a Cloud endpoint survived: %q", unwanted)
		}
	}
	if !strings.Contains(cloud[0], "fan out") {
		t.Errorf("cloud: the note should say the per-replica tables fan out, got %q", cloud[0])
	}

	for _, mode := range []string{"onprem", "gov", ""} {
		got := nodeWarnings(lb, "ch-lb.internal", "laptop.local", mode, false)
		if len(got) != 1 || !strings.HasPrefix(got[0], "Warning:") {
			t.Errorf("mode %q: a load balancer must stay a warning, got %v", mode, got)
		}
	}

	// The mixed-host warning is unaffected by the mode.
	mixed := nodeWarnings(nodeIdentity{Host: "ch-01", FQDN: "ch-01.internal"}, "ch-01.internal", "laptop.local", "cloud", true)
	if len(mixed) != 1 || !strings.HasPrefix(mixed[0], "Warning:") {
		t.Errorf("host facts from another machine must stay a warning in every mode, got %v", mixed)
	}
}
