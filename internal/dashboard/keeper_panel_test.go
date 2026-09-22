package dashboard

import (
	"strings"
	"testing"
)

// The Keeper Health panel runs live queries against metric_log — one row per
// second per server, ~1700 columns. It stays cheap only while it reads a few
// narrow columns over a bounded window and aggregates per hour; these are the
// bounds a refactor must not lose.
func TestKeeperMetricSQL_IsBoundedAndDegrades(t *testing.T) {
	g := &Generator{mode: "onprem"}
	full := g.keeperMetricSQL(true, true)
	for _, want := range []string{
		"INTERVAL 7 DAY",
		"toStartOfHour(event_time)",
		"GROUP BY time",
		"ProfileEvent_ZooKeeperTransactions",
		"ProfileEvent_ZooKeeperHardwareExceptions",
		"ProfileEvent_ZooKeeperWaitMicroseconds",
		"CurrentMetric_ZooKeeperSession",
		"FROM system.metric_log",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("keeper metric query lost %q:\n%s", want, full)
		}
	}
	// Versions without the optional columns get constants under the same
	// output names, so the panel JS never sees a missing key.
	degraded := g.keeperMetricSQL(false, false)
	for _, banned := range []string{"ProfileEvent_ZooKeeperWaitMicroseconds", "CurrentMetric_ZooKeeperSession"} {
		if strings.Contains(degraded, banned) {
			t.Errorf("degraded query must not reference %s:\n%s", banned, degraded)
		}
	}
	for _, want := range []string{"AS wait_us", "AS sessions_min", "AS sessions_max"} {
		if !strings.Contains(degraded, want) {
			t.Errorf("degraded query must keep output name %q:\n%s", want, degraded)
		}
	}
	if !strings.Contains((&Generator{mode: "cloud"}).keeperMetricSQL(true, true), "clusterAllReplicas(default, system.metric_log)") {
		t.Error("cloud mode must fan metric_log out over the replicas")
	}
}

// error_log counts every thread; query_log only queries. The fallback must
// exist (error_log needs 24.8+) and both must stay bounded to the same codes
// and window so the two shapes are comparable.
// The latency panel must distinguish "this version does not export
// ZooKeeperWaitMicroseconds" from "the column exists and the server simply
// never contacted Keeper". The degraded query substitutes `0 AS wait_us`, so
// an all-zero series looks identical in both cases and the JS cannot infer
// which it is — collect() publishes keeper_wait_available for that, and the
// template must branch on it rather than blame the server version.
func TestKeeperPanel_WaitUnavailableIsNotConfusedWithNoKeeper(t *testing.T) {
	html := buildHTML(keeperIncidentFixture())
	for _, want := range []string{
		"DATA.keeper_wait_available!==false",
		"not exported by this version",
		"No Keeper wait time recorded in this window",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("keeper latency panel missing %q", want)
		}
	}
	// The version message must not be reachable while the column is present:
	// it may only appear inside the false branch of the waitCol conditional.
	i := strings.Index(html, "DATA.keeper_wait_available!==false")
	j := strings.Index(html, "No Keeper wait time recorded in this window")
	k := strings.Index(html, "not exported by this version")
	if !(i < j && j < k) {
		t.Errorf("expected waitCol test, then the no-requests message, then the version message; got %d, %d, %d", i, j, k)
	}
}

func TestKeeperErrorsSQL_SourceSwitch(t *testing.T) {
	g := &Generator{mode: "onprem"}
	withErrLog := g.keeperErrorsSQL(true)
	fallback := g.keeperErrorsSQL(false)
	if !strings.Contains(withErrLog, "FROM system.error_log") || strings.Contains(withErrLog, "query_log") {
		t.Errorf("error_log variant reads the wrong table:\n%s", withErrLog)
	}
	if !strings.Contains(fallback, "FROM system.query_log") || !strings.Contains(fallback, "errorCodeToName(exception_code)") {
		t.Errorf("query_log fallback must read query_log and name the code:\n%s", fallback)
	}
	for _, q := range []string{withErrLog, fallback} {
		for _, want := range []string{"INTERVAL 7 DAY", "999, 242, 319, 571, 252", "AS code_name", "AS count", "GROUP BY time, code_name"} {
			if !strings.Contains(q, want) {
				t.Errorf("keeper errors query lost %q:\n%s", want, q)
			}
		}
	}
}

func TestKeeperConnectionSQL_CloudNamesReplica(t *testing.T) {
	if strings.Contains((&Generator{mode: "onprem"}).keeperConnectionSQL(), "hostName()") {
		t.Error("onprem connection query must not add a replica column")
	}
	cloud := (&Generator{mode: "cloud"}).keeperConnectionSQL()
	if !strings.Contains(cloud, "hostName() AS replica") || !strings.Contains(cloud, "clusterAllReplicas(default, system.zookeeper_connection)") {
		t.Errorf("cloud connection query must name the replica and fan out:\n%s", cloud)
	}
}

// The panel is conditional (hidden without metric_log rows) and its verdict
// rule must be the one the alert and the skill use, so the three surfaces
// never disagree about an hour.
func TestTemplate_KeeperPanel(t *testing.T) {
	for _, want := range []string{
		`id="sec-keeper"`, `id="nav-keeper"`,
		"const rows=DATA.keeper_metric_hourly||[];",
		"if(!rows.length)return;",
		"chart-keeper-traffic", "chart-keeper-exceptions", "chart-keeper-latency", "chart-keeper-errors",
		"tbl-keeper-verdict", "tbl-keeper-connection",
		"hw[i]>1000&&pct<50)return 'UNAVAILABLE'",                                 // UNAVAILABLE: exceptions AND traffic collapse
		"if(pct===null)return hw[i]>1000?'exceptions, no traffic baseline':'ok';", // no baseline → never a blip
		"DATA.keeper_errors_source",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("keeper panel missing %q", want)
		}
	}
}

// Replica Details is one row per replicated table (25 000 on a large shared
// cluster). The charts may use every row; the table must page so the DOM
// does not, and the first page must be the replicas that need attention.
func TestTemplate_ReplicasTableIsPaginated(t *testing.T) {
	for _, want := range []string{
		`id="replicas-pagination"`, `id="replicas-scope"`,
		"const REPLICA_PAGE=50;",
		"rows.slice(start,start+REPLICA_PAGE)",
		"window._replicaPg",
		"const rows=[...allRows].sort((a,b)=>rank(b)-rank(a));", // attention rows first, before paging
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("replicas table pagination missing %q", want)
		}
	}
	if strings.Contains(htmlTemplate, "renderTable('tbl-replicas',rows,") {
		t.Error("replicas table renders every row again — page it")
	}
}
