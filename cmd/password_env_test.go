package main

import "testing"

// -password 必须一如既往地可用；CLICKHOUSE_PASSWORD 只在标志缺失时补位，
// 且优先于交互式提示。cluster 命令依赖这一点：每个按主机的运行都继承该环境
// 变量，因此密码永远不会出现在命令行上。
func TestResolvePassword(t *testing.T) {
	env := func(v string, set bool) func(string) (string, bool) {
		return func(k string) (string, bool) {
			if k != passwordEnvVar {
				t.Fatalf("查询了 %q，期望 %q", k, passwordEnvVar)
			}
			return v, set
		}
	}
	cases := []struct {
		name, flag string
		flagSet    bool
		lookup     func(string) (string, bool)
		want       string
		wantFound  bool
	}{
		{"flag wins over the environment", "from-flag", true, env("from-env", true), "from-flag", true},
		// 显式的 -password "" 在 CLICKHOUSE_PASSWORD 出现之前会触发提示，
		// 即使导出了另一个客户端的密码，也必须继续如此。
		{"explicit empty flag still prompts", "", true, env("from-env", true), "", false},
		{"environment when no flag", "", false, env("from-env", true), "from-env", true},
		{"empty but set is an explicit empty password", "", false, env("", true), "", true},
		{"neither: prompt", "", false, env("", false), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := resolvePassword(c.flag, c.flagSet, c.lookup)
			if got != c.want || found != c.wantFound {
				t.Errorf("resolvePassword(%q) = %q, %v；期望 %q, %v", c.flag, got, found, c.want, c.wantFound)
			}
		})
	}
}
