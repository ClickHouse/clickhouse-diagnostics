package main

import (
	"errors"
	"testing"
)

// Review finding on #36: every error from the visibility probe was reported
// as a privilege problem with GRANT advice. Only the server's own denial is.
func TestDatabaseVisibilityVerdict(t *testing.T) {
	cases := []struct {
		name  string
		count string
		err   error
		want  visibilityVerdict
	}{
		{"user databases visible", "3\n", nil, visibilityOK},
		{"SELECT on system.* without SHOW: count is zero", "0\n", nil, visibilityNone},
		{"no grants at all: server refuses with 497", "",
			errors.New("non-OK status: 403, body: Code: 497. DB::Exception: sysonly: Not enough privileges. To execute this query, it's necessary to have the grant SHOW DATABASES ON *.*. (ACCESS_DENIED) (version 26.2.19.43 (official build))"),
			visibilityDenied},
		{"transient network failure is NOT a grants problem", "",
			errors.New("error executing request: Post \"http://ch-01:8123/\": dial tcp: i/o timeout"), visibilityUnknown},
		{"server-side timeout is NOT a grants problem", "",
			errors.New("non-OK status: 500, body: Code: 159. DB::Exception: Timeout exceeded: elapsed 240 seconds. (TIMEOUT_EXCEEDED)"), visibilityUnknown},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := databaseVisibilityVerdict(c.count, c.err); got != c.want {
				t.Errorf("verdict = %v, want %v", got, c.want)
			}
		})
	}
	if isAccessDenied(nil) {
		t.Error("nil error must not read as access denied")
	}
}
