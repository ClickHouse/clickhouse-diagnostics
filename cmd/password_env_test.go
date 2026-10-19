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
		flagSet    bool
		lookup     func(string) (string, bool)
		want       string
		wantFound  bool
	}{
		{"flag wins over the environment", "from-flag", true, env("from-env", true), "from-flag", true},
		// An explicit -password "" prompted before CLICKHOUSE_PASSWORD existed,
		// and must keep doing so even when another client's password is exported.
		{"explicit empty flag still prompts", "", true, env("from-env", true), "", false},
		{"environment when no flag", "", false, env("from-env", true), "from-env", true},
		{"empty but set is an explicit empty password", "", false, env("", true), "", true},
		{"neither: prompt", "", false, env("", false), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := resolvePassword(c.flag, c.flagSet, c.lookup)
			if got != c.want || found != c.wantFound {
				t.Errorf("resolvePassword(%q) = %q, %v; want %q, %v", c.flag, got, found, c.want, c.wantFound)
			}
		})
	}
}
