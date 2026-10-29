package collectmode

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func healthy() Signals {
	return Signals{Ping: 20 * time.Millisecond, MetricsRead: 40 * time.Millisecond,
		Queries: 5, MaxConcurrentQueries: 1000, MemoryTracking: 2 << 30, MemoryLimit: 50 << 30,
		Load1: 3, CPUs: 16, ReadonlyReplicas: 0, KeeperSessions: 1, Tables: 300}
}

func TestDecide_AutoOnAHealthyServerIsFull(t *testing.T) {
	d := Decide(Auto, healthy(), nil)
	if d.Mode != Full || len(d.Reasons) != 0 || d.Large {
		t.Fatalf("healthy server: %+v", d)
	}
}

func TestDecide_AutoPicksEssentialAndSaysWhy(t *testing.T) {
	cases := map[string]func(*Signals){
		"answer":         func(s *Signals) { s.Ping = 3200 * time.Millisecond },
		"system.metrics": func(s *Signals) { s.MetricsRead = 2500 * time.Millisecond },
		"concurrent":     func(s *Signals) { s.Queries = 850 },
		"memory":         func(s *Signals) { s.MemoryTracking = 47 << 30 },
		"load":           func(s *Signals) { s.Load1 = 40 },
		"read-only":      func(s *Signals) { s.ReadonlyReplicas = 3 },
	}
	for want, mutate := range cases {
		s := healthy()
		mutate(&s)
		d := Decide(Auto, s, nil)
		if d.Mode != Essential {
			t.Errorf("%s: mode %s, want essential", want, d.Mode)
			continue
		}
		if !strings.Contains(strings.Join(d.Reasons, " | "), want) {
			t.Errorf("%s: reasons %q do not name it", want, d.Reasons)
		}
	}
}

// Size alone is not stress: a server with tens of thousands of tables is
// "large" (cheaper per-table reads apply) but still collected in full.
func TestDecide_LargeIsNotEssential(t *testing.T) {
	s := healthy()
	s.Tables = 34000
	d := Decide(Auto, s, nil)
	if d.Mode != Full || !d.Large {
		t.Fatalf("large healthy server: %+v", d)
	}
}

// Unknown limits never trigger: no max_concurrent_queries / memory limit /
// CPU count means the ratio cannot be computed, not that it is exceeded.
func TestDecide_MissingLimitsDoNotTrigger(t *testing.T) {
	s := Signals{Ping: 10 * time.Millisecond, MetricsRead: 10 * time.Millisecond, Queries: 900, MemoryTracking: 90 << 30, Load1: 99}
	if d := Decide(Auto, s, nil); d.Mode != Full {
		t.Fatalf("no limits known: %+v", d)
	}
}

func TestDecide_OverridesWin(t *testing.T) {
	s := healthy()
	s.Ping = 5 * time.Second
	if d := Decide(Full, s, nil); d.Mode != Full || !strings.Contains(d.Summary(), "-collect full") {
		t.Errorf("forced full on a struggling server: %+v", d)
	}
	if d := Decide(Essential, healthy(), nil); d.Mode != Essential || !strings.Contains(d.Summary(), "-collect essential") {
		t.Errorf("forced essential: %+v", d)
	}
}

// When the probe itself cannot read the server, that is a reason to be
// gentle, not a reason to fail the run.
func TestDecide_ProbeErrorMeansEssential(t *testing.T) {
	d := Decide(Auto, Signals{}, errors.New("Code: 159. Timeout exceeded"))
	if d.Mode != Essential || !strings.Contains(strings.Join(d.Reasons, " "), "probe") {
		t.Fatalf("probe failed: %+v", d)
	}
}

func TestWatchdog_TimeoutTripsAndSaysWhy(t *testing.T) {
	w := NewWatchdog(0)
	w.Observe("system.parts.sql", 2*time.Second, nil)
	if tripped, _ := w.Tripped(); tripped {
		t.Fatal("a fast query tripped the watchdog")
	}
	w.Observe("system.detached_parts.sql", 300*time.Second, errors.New(`error executing request: Post "http://h:8123/": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`))
	tripped, why := w.Tripped()
	if !tripped || !strings.Contains(why, "system.detached_parts.sql") || !strings.Contains(why, "timed out") {
		t.Fatalf("timeout did not trip: %v %q", tripped, why)
	}
}

func TestWatchdog_TwoSlowInARowTrip(t *testing.T) {
	w := NewWatchdog(0)
	w.Observe("a", 31*time.Second, nil)
	w.Observe("b", time.Second, nil) // breaks the streak
	w.Observe("c", 31*time.Second, nil)
	if tripped, _ := w.Tripped(); tripped {
		t.Fatal("two slow queries that were not consecutive tripped it")
	}
	w.Observe("d", 45*time.Second, nil)
	if tripped, why := w.Tripped(); !tripped || !strings.Contains(why, "c") || !strings.Contains(why, "d") {
		t.Fatalf("two slow in a row did not trip: %q", why)
	}
}

func TestWatchdog_ServerSideTimeoutCountsToo(t *testing.T) {
	w := NewWatchdog(0)
	w.Observe("q", 30*time.Second, errors.New("non-OK status: 500, body: Code: 159. DB::Exception: Timeout exceeded: elapsed 30.0 seconds, maximum: 30. (TIMEOUT_EXCEEDED)"))
	if tripped, _ := w.Tripped(); !tripped {
		t.Fatal("TIMEOUT_EXCEEDED did not trip")
	}
	w2 := NewWatchdog(0)
	w2.Observe("q", time.Second, errors.New("Code: 60. Unknown table"))
	if tripped, _ := w2.Tripped(); tripped {
		t.Fatal("an ordinary error tripped the watchdog")
	}
}

func TestWatchdog_Budget(t *testing.T) {
	now := time.Unix(1000, 0)
	w := NewWatchdog(3 * time.Minute)
	w.now = func() time.Time { return now }
	w.Start()
	if w.OverBudget() {
		t.Fatal("over budget at start")
	}
	now = now.Add(3*time.Minute + time.Second)
	if !w.OverBudget() {
		t.Fatal("budget not enforced")
	}
	if NewWatchdog(0).OverBudget() {
		t.Fatal("zero budget means unbounded")
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Mode{"": Auto, "auto": Auto, "ESSENTIAL": Essential, "full": Full} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := Parse("fast"); err == nil {
		t.Error("Parse accepted an unknown mode")
	}
}

type fakeRunner struct {
	answers map[string]string
	fail    map[string]error
	delay   time.Duration
}

func (f fakeRunner) ExecuteQueryReal(q string) (string, error) {
	time.Sleep(f.delay)
	for k, err := range f.fail {
		if strings.Contains(q, k) {
			return "", err
		}
	}
	for k, a := range f.answers {
		if strings.Contains(q, k) {
			return a, nil
		}
	}
	return "1\n", nil
}

func TestProbe_ReadsSignalsAndDerivesTheMemoryLimit(t *testing.T) {
	r := fakeRunner{answers: map[string]string{
		"system.metrics":         "850\t48318382080\t2\t40.5\t34000\t68719476736\t16\t0\t0\n",
		"system.server_settings": "1000\t0\t0.9\n",
	}}
	s, err := Probe(r)
	if err != nil {
		t.Fatal(err)
	}
	if s.Queries != 850 || s.ReadonlyReplicas != 2 || s.Load1 != 40.5 || s.Tables != 34000 || s.CPUs != 16 || s.MaxConcurrentQueries != 1000 {
		t.Fatalf("signals: %+v", s)
	}
	ram := 68719476736.0
	if want := int64(0.9 * ram); s.MemoryLimit != want {
		t.Fatalf("memory limit %d, want %d (RAM × ratio when max_server_memory_usage is 0)", s.MemoryLimit, want)
	}
	if d := Decide(Auto, s, nil); d.Mode != Essential || len(d.Reasons) < 3 || !d.Large {
		t.Fatalf("decision on these signals: %+v", d)
	}
}

// 22.8 has no system.server_settings: the limits stay unknown, the probe
// still succeeds.
func TestProbe_OldServerWithoutServerSettings(t *testing.T) {
	r := fakeRunner{answers: map[string]string{"system.metrics": "5\t1000\t0\t0.5\t10\t0\t8\n"},
		fail: map[string]error{"system.server_settings": errors.New("Code: 60. Unknown table system.server_settings")}}
	s, err := Probe(r)
	if err != nil || s.MaxConcurrentQueries != 0 || s.MemoryLimit != 0 || s.CPUs != 8 {
		t.Fatalf("old server: %+v %v", s, err)
	}
}

func TestProbe_FailureIsReported(t *testing.T) {
	r := fakeRunner{fail: map[string]error{"SELECT 1": errors.New("connection refused")}}
	if _, err := Probe(r); err == nil {
		t.Fatal("a failed SELECT 1 was not reported")
	}
}

// Tripping mid-run starts the essential budget then, and tells the caller
// once, so it can apply the gentle settings from that query on.
func TestWatchdog_TripStartsBudgetAndNotifiesOnce(t *testing.T) {
	now := time.Unix(1000, 0)
	w := NewWatchdog(0).WithTripBudget(3 * time.Minute)
	w.now = func() time.Time { return now }
	w.Start()
	calls := 0
	w.OnTrip(func(reason string) { calls++ })
	now = now.Add(10 * time.Minute)
	if w.OverBudget() {
		t.Fatal("a full run has no budget before it trips")
	}
	w.Observe("q", time.Second, errors.New("Code: 159. TIMEOUT_EXCEEDED"))
	w.Trip("again")
	if calls != 1 {
		t.Fatalf("OnTrip called %d times", calls)
	}
	now = now.Add(2 * time.Minute)
	if w.OverBudget() {
		t.Fatal("budget counted from the start instead of the trip")
	}
	now = now.Add(2 * time.Minute)
	if !w.OverBudget() {
		t.Fatal("trip budget not enforced")
	}
}

func TestWatchdog_LockedNeverTrips(t *testing.T) {
	w := NewWatchdog(0).Locked()
	w.Observe("q", time.Second, errors.New("Code: 159. TIMEOUT_EXCEEDED"))
	w.Trip("essential chosen")
	if tripped, _ := w.Tripped(); tripped {
		t.Fatal("-collect full switched to essential")
	}
}

// No Keeper session while tables are read-only: Keeper is down, and the
// queries that need a session would only wait for their timeout.
func TestDecide_KeeperDown(t *testing.T) {
	s := healthy()
	s.ReadonlyReplicas, s.KeeperSessions = 2001, 0
	d := Decide(Auto, s, nil)
	if d.Mode != Essential || !d.KeeperDown || !strings.Contains(strings.Join(d.Reasons, " "), "no Keeper session") {
		t.Fatalf("Keeper down: %+v", d)
	}
	// The session object survives a lost connection: the loss timestamp says
	// Keeper is gone even though ZooKeeperSession reads 1.
	lost := healthy()
	lost.ReadonlyReplicas, lost.KeeperSessions, lost.KeeperLossSince = 2001, 1, 1793288917
	if d := Decide(Auto, lost, nil); !d.KeeperDown || !strings.Contains(strings.Join(d.Reasons, " "), "connection lost since") {
		t.Fatalf("lost connection with a session object: %+v", d)
	}
	s.KeeperSessions = 1 // read-only for another reason (disk): Keeper answers
	if d := Decide(Auto, s, nil); d.KeeperDown {
		t.Fatalf("a live session counted as Keeper down: %+v", d)
	}
	n := healthy()
	n.KeeperSessions = 0 // no Keeper configured at all, nothing read-only
	if d := Decide(Auto, n, nil); d.KeeperDown || d.Mode != Full {
		t.Fatalf("a server without Keeper counted as Keeper down: %+v", d)
	}
}
