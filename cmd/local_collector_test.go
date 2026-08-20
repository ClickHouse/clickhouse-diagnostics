package main

import "testing"

// TestResolveLocalCollector pins the mode-dependent defaults for the two
// collectors that read the LOCAL filesystem. The table is the specification:
// onprem collects by default because the tool runs on the server; cloud does
// not because it runs somewhere else; gov never does, at any setting.
func TestResolveLocalCollector(t *testing.T) {
	cases := []struct {
		setting  string
		mode     string
		wantSkip bool
		wantErr  bool
	}{
		// auto — the whole point of the change
		{"auto", "onprem", false, false},
		{"auto", "cloud", true, false},
		{"auto", "gov", true, false},
		{"", "onprem", false, false}, // empty behaves as auto
		{"", "cloud", true, false},

		// explicit on — allowed everywhere except gov
		{"on", "onprem", false, false},
		{"on", "cloud", false, false},
		{"on", "gov", true, true}, // refused, AND still skipped

		// explicit off — always honoured, never an error
		{"off", "onprem", true, false},
		{"off", "cloud", true, false},
		{"off", "gov", true, false},

		// tolerate the shapes users actually type
		{"ON", "onprem", false, false},
		{" auto ", "cloud", true, false},

		// mode is normalised INSIDE the resolver too. The call site is
		// supposed to pass the post-getUserInput value, but this function
		// gates the gov privacy guarantee — "-mode GOV -host-info=on" once
		// slipped host facts into a gov bundle via this exact hole.
		{"on", "GOV", true, true},
		{"on", " Gov ", true, true},
		{"auto", "ONPREM", false, false},
		{"auto", "Cloud", true, false},

		// anything else is a usage error, and must fail closed
		{"true", "onprem", true, true},
		{"yes", "onprem", true, true},
	}

	for _, c := range cases {
		skip, err := resolveLocalCollector(c.setting, c.mode, "host-info")
		if skip != c.wantSkip {
			t.Errorf("resolveLocalCollector(%q, %q) skip = %v, want %v",
				c.setting, c.mode, skip, c.wantSkip)
		}
		if (err != nil) != c.wantErr {
			t.Errorf("resolveLocalCollector(%q, %q) err = %v, wantErr %v",
				c.setting, c.mode, err, c.wantErr)
		}
	}
}

// TestResolveLocalCollector_GovNeverCollects states the security-relevant
// half of the table on its own, so that deleting a row above cannot quietly
// weaken it. No setting may make gov mode collect host facts or log files.
func TestResolveLocalCollector_GovNeverCollects(t *testing.T) {
	for _, setting := range []string{"auto", "on", "ON", "off", "", "garbage"} {
		for _, mode := range []string{"gov", "GOV", " gov ", "Gov"} {
			for _, name := range []string{"host-info", "logs"} {
				if skip, _ := resolveLocalCollector(setting, mode, name); !skip {
					t.Errorf("gov mode (as %q) collected %s with --%s=%q; gov must "+
						"never read the local filesystem", mode, name, name, setting)
				}
			}
		}
	}
}
