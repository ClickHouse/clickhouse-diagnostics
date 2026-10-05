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
	w := nodeWarnings(lb, "clickhouse-lb.example", "ch-09.x", false)
	if len(w) != 1 || !strings.Contains(w[0], "load balancer") || !strings.Contains(w[0], "ch-09 and ch-20") {
		t.Errorf("load balancer warning missing: %v", w)
	}

	remote := nodeIdentity{Host: "ch-03", FQDN: "ch-03.x", Samples: []string{"ch-03", "ch-03", "ch-03"}}
	w = nodeWarnings(remote, "ch-03.x", "ops-laptop", true)
	if len(w) != 1 || !strings.Contains(w[0], "read from THIS machine (ops-laptop)") {
		t.Errorf("remote-host warning missing: %v", w)
	}
	// Same machine, or loopback, or not collecting local files: quiet.
	if w := nodeWarnings(remote, "ch-03.x", "ch-03", true); len(w) != 0 {
		t.Errorf("same machine must not warn: %v", w)
	}
	if w := nodeWarnings(remote, "localhost", "ops-laptop", true); len(w) != 0 {
		t.Errorf("loopback must not warn: %v", w)
	}
	if w := nodeWarnings(remote, "ch-03.x", "ops-laptop", false); len(w) != 0 {
		t.Errorf("no local files collected, nothing to mix: %v", w)
	}
	if w := nodeWarnings(nodeIdentity{Err: errors.New("no grant")}, "x", "y", true); len(w) != 0 {
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
