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
		t.Fatalf("健康服务器：%+v", d)
	}
}

func TestDecide_AutoPicksEssentialAndSaysWhy(t *testing.T) {
	cases := map[string]func(*Signals){
		"响应 SELECT 1":            func(s *Signals) { s.Ping = 3200 * time.Millisecond },
		"system.metrics":         func(s *Signals) { s.MetricsRead = 2500 * time.Millisecond },
		"max_concurrent_queries": func(s *Signals) { s.Queries = 850 },
		"内存已达":                   func(s *Signals) { s.MemoryTracking = 47 << 30 },
		"load average":           func(s *Signals) { s.Load1 = 40 },
		"只读状态":                   func(s *Signals) { s.ReadonlyReplicas = 3 },
	}
	for want, mutate := range cases {
		s := healthy()
		mutate(&s)
		d := Decide(Auto, s, nil)
		if d.Mode != Essential {
			t.Errorf("%s：mode %s，期望 essential", want, d.Mode)
			continue
		}
		if !strings.Contains(strings.Join(d.Reasons, " | "), want) {
			t.Errorf("%s：reasons %q 未提及它", want, d.Reasons)
		}
	}
}

// 规模本身不是压力：拥有数万张表的服务器是
// “大”（适用更便宜的逐表读取），但仍会完整采集。
func TestDecide_LargeIsNotEssential(t *testing.T) {
	s := healthy()
	s.Tables = 34000
	d := Decide(Auto, s, nil)
	if d.Mode != Full || !d.Large {
		t.Fatalf("大型健康服务器：%+v", d)
	}
}

// 未知限制绝不触发：没有 max_concurrent_queries / 内存限制 /
// CPU 数意味着无法计算比率，而不是比率被超出。
func TestDecide_MissingLimitsDoNotTrigger(t *testing.T) {
	s := Signals{Ping: 10 * time.Millisecond, MetricsRead: 10 * time.Millisecond, Queries: 900, MemoryTracking: 90 << 30, Load1: 99}
	if d := Decide(Auto, s, nil); d.Mode != Full {
		t.Fatalf("未知任何限制：%+v", d)
	}
}

func TestDecide_OverridesWin(t *testing.T) {
	s := healthy()
	s.Ping = 5 * time.Second
	if d := Decide(Full, s, nil); d.Mode != Full || !strings.Contains(d.Summary(), "-collect full") {
		t.Errorf("在处于困境的服务器上强制 full：%+v", d)
	}
	if d := Decide(Essential, healthy(), nil); d.Mode != Essential || !strings.Contains(d.Summary(), "-collect essential") {
		t.Errorf("强制 essential：%+v", d)
	}
}

// 当探测本身无法读取服务器时，这是应当温和处理的理由，
// 而不是让运行失败的理由。
func TestDecide_ProbeErrorMeansEssential(t *testing.T) {
	d := Decide(Auto, Signals{}, errors.New("Code: 159. Timeout exceeded"))
	if d.Mode != Essential || !strings.Contains(strings.Join(d.Reasons, " "), "探测") {
		t.Fatalf("探测失败：%+v", d)
	}
}

func TestWatchdog_TimeoutTripsAndSaysWhy(t *testing.T) {
	w := NewWatchdog(0)
	w.Observe("system.parts.sql", 2*time.Second, nil)
	if tripped, _ := w.Tripped(); tripped {
		t.Fatal("一条快速查询触发了 watchdog")
	}
	w.Observe("system.detached_parts.sql", 300*time.Second, errors.New(`error executing request: Post "http://h:8123/": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`))
	tripped, why := w.Tripped()
	if !tripped || !strings.Contains(why, "system.detached_parts.sql") || !strings.Contains(why, "timed out") {
		t.Fatalf("超时未触发：%v %q", tripped, why)
	}
}

func TestWatchdog_TwoSlowInARowTrip(t *testing.T) {
	w := NewWatchdog(0)
	w.Observe("a", 31*time.Second, nil)
	w.Observe("b", time.Second, nil) // 打断连续计数
	w.Observe("c", 31*time.Second, nil)
	if tripped, _ := w.Tripped(); tripped {
		t.Fatal("两条不连续的慢查询触发了它")
	}
	w.Observe("d", 45*time.Second, nil)
	if tripped, why := w.Tripped(); !tripped || !strings.Contains(why, "c") || !strings.Contains(why, "d") {
		t.Fatalf("连续两条慢查询未触发：%q", why)
	}
}

func TestWatchdog_ServerSideTimeoutCountsToo(t *testing.T) {
	w := NewWatchdog(0)
	w.Observe("q", 30*time.Second, errors.New("non-OK status: 500, body: Code: 159. DB::Exception: Timeout exceeded: elapsed 30.0 seconds, maximum: 30. (TIMEOUT_EXCEEDED)"))
	if tripped, _ := w.Tripped(); !tripped {
		t.Fatal("TIMEOUT_EXCEEDED 未触发")
	}
	w2 := NewWatchdog(0)
	w2.Observe("q", time.Second, errors.New("Code: 60. Unknown table"))
	if tripped, _ := w2.Tripped(); tripped {
		t.Fatal("普通错误触发了 watchdog")
	}
}

func TestWatchdog_Budget(t *testing.T) {
	now := time.Unix(1000, 0)
	w := NewWatchdog(3 * time.Minute)
	w.now = func() time.Time { return now }
	w.Start()
	if w.OverBudget() {
		t.Fatal("开始时即超出预算")
	}
	now = now.Add(3*time.Minute + time.Second)
	if !w.OverBudget() {
		t.Fatal("预算未被执行")
	}
	if NewWatchdog(0).OverBudget() {
		t.Fatal("零预算表示无上限")
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Mode{"": Auto, "auto": Auto, "ESSENTIAL": Essential, "full": Full} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %v，%v", in, got, err)
		}
	}
	if _, err := Parse("fast"); err == nil {
		t.Error("Parse 接受了未知模式")
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
		t.Fatalf("信号：%+v", s)
	}
	ram := 68719476736.0
	if want := int64(0.9 * ram); s.MemoryLimit != want {
		t.Fatalf("内存限制 %d，期望 %d（max_server_memory_usage 为 0 时取 RAM × 比率）", s.MemoryLimit, want)
	}
	if d := Decide(Auto, s, nil); d.Mode != Essential || len(d.Reasons) < 3 || !d.Large {
		t.Fatalf("基于这些信号的决策：%+v", d)
	}
}

// 22.8 没有 system.server_settings：限制保持未知，探测
// 仍然成功。
func TestProbe_OldServerWithoutServerSettings(t *testing.T) {
	r := fakeRunner{answers: map[string]string{"system.metrics": "5\t1000\t0\t0.5\t10\t0\t8\n"},
		fail: map[string]error{"system.server_settings": errors.New("Code: 60. Unknown table system.server_settings")}}
	s, err := Probe(r)
	if err != nil || s.MaxConcurrentQueries != 0 || s.MemoryLimit != 0 || s.CPUs != 8 {
		t.Fatalf("旧版服务器：%+v %v", s, err)
	}
}

func TestProbe_FailureIsReported(t *testing.T) {
	r := fakeRunner{fail: map[string]error{"SELECT 1": errors.New("connection refused")}}
	if _, err := Probe(r); err == nil {
		t.Fatal("失败的 SELECT 1 未被报告")
	}
}

// 运行中途切换会在那时启动 essential 预算，并只通知调用方
// 一次，使其能从该查询开始应用温和设置。
func TestWatchdog_TripStartsBudgetAndNotifiesOnce(t *testing.T) {
	now := time.Unix(1000, 0)
	w := NewWatchdog(0).WithTripBudget(3 * time.Minute)
	w.now = func() time.Time { return now }
	w.Start()
	calls := 0
	w.OnTrip(func(reason string) { calls++ })
	now = now.Add(10 * time.Minute)
	if w.OverBudget() {
		t.Fatal("完整运行在切换前没有预算")
	}
	w.Observe("q", time.Second, errors.New("Code: 159. TIMEOUT_EXCEEDED"))
	w.Trip("again")
	if calls != 1 {
		t.Fatalf("OnTrip 被调用了 %d 次", calls)
	}
	now = now.Add(2 * time.Minute)
	if w.OverBudget() {
		t.Fatal("预算从起点而不是切换时刻开始计算")
	}
	now = now.Add(2 * time.Minute)
	if !w.OverBudget() {
		t.Fatal("切换预算未被执行")
	}
}

func TestWatchdog_LockedNeverTrips(t *testing.T) {
	w := NewWatchdog(0).Locked()
	w.Observe("q", time.Second, errors.New("Code: 159. TIMEOUT_EXCEEDED"))
	w.Trip("essential chosen")
	if tripped, _ := w.Tripped(); tripped {
		t.Fatal("-collect full 被切换成了 essential")
	}
}

// 表处于只读状态时没有 Keeper 会话：Keeper 已宕机，
// 需要会话的查询只会白白等待超时。
func TestDecide_KeeperDown(t *testing.T) {
	s := healthy()
	s.ReadonlyReplicas, s.KeeperSessions = 2001, 0
	d := Decide(Auto, s, nil)
	if d.Mode != Essential || !d.KeeperDown || !strings.Contains(strings.Join(d.Reasons, " "), "没有 Keeper 会话") {
		t.Fatalf("Keeper 宕机：%+v", d)
	}
	// 会话对象在连接丢失后仍会存活：尽管 ZooKeeperSession 读数为 1，
	// 丢失时间戳表明 Keeper 已不可用。
	lost := healthy()
	lost.ReadonlyReplicas, lost.KeeperSessions, lost.KeeperLossSince = 2001, 1, 1793288917
	if d := Decide(Auto, lost, nil); !d.KeeperDown || !strings.Contains(strings.Join(d.Reasons, " "), "连接自") {
		t.Fatalf("会话对象存在但连接已丢失：%+v", d)
	}
	s.KeeperSessions = 1 // 因其他原因（磁盘）只读：Keeper 能应答
	if d := Decide(Auto, s, nil); d.KeeperDown {
		t.Fatalf("活动会话被判定为 Keeper 宕机：%+v", d)
	}
	n := healthy()
	n.KeeperSessions = 0 // 完全没有配置 Keeper，也没有任何只读
	if d := Decide(Auto, n, nil); d.KeeperDown || d.Mode != Full {
		t.Fatalf("未配置 Keeper 的服务器被判定为 Keeper 宕机：%+v", d)
	}
}
