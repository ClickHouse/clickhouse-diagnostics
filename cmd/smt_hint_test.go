package main

import (
	"strings"
	"testing"
)

func TestSharedMergeTreeHint(t *testing.T) {
	cases := []struct {
		mode, value string
		want        bool
	}{
		{"onprem", "1\n", true},
		{"onprem", "true", true},
		{"ONPREM ", " 1 ", true},
		{"onprem", "0", false},
		{"onprem", "", false},
		{"onprem", "garbage", false},
		{"cloud", "1", false},
		{"gov", "1", false},
	}
	for _, c := range cases {
		got := sharedMergeTreeHint(c.mode, c.value)
		if (got != "") != c.want {
			t.Errorf("mode=%q value=%q: got %q, want hint=%v", c.mode, c.value, got, c.want)
		}
		if got != "" && !strings.Contains(got, "-mode cloud") {
			t.Errorf("hint must name the alternative mode: %q", got)
		}
	}
}
