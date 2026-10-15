package main

import "testing"

// -password must keep working exactly as before; CLICKHOUSE_PASSWORD only
// fills in when the flag is absent, ahead of the interactive prompt. The
// cluster command relies on it: every per-host run inherits the variable, so
// the password never appears on a command line.
func TestResolvePassword(t *testing.T) {
	env := func(v string, set bool) func(string) (string, bool) {
		return func(k string) (string, bool) {
			if k != passwordEnvVar {
				t.Fatalf("looked up %q, want %q", k, passwordEnvVar)
			}
			return v, set
		}
	}
	cases := []struct {
		name, flag string
		lookup     func(string) (string, bool)
		want       string
		wantFound  bool
	}{
		{"flag wins over the environment", "from-flag", env("from-env", true), "from-flag", true},
		{"environment when no flag", "", env("from-env", true), "from-env", true},
		{"empty but set is an explicit empty password", "", env("", true), "", true},
		{"neither: prompt", "", env("", false), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := resolvePassword(c.flag, c.lookup)
			if got != c.want || found != c.wantFound {
				t.Errorf("resolvePassword(%q) = %q, %v; want %q, %v", c.flag, got, found, c.want, c.wantFound)
			}
		})
	}
}
