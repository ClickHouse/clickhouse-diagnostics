package query

import (
	"fmt"
	"regexp"
)

// Tiers of a collector. Essential collectors are the cheap ones that decide
// most incidents: they run first in every collection, and are the only ones
// an essential collection runs (see internal/collectmode).
const (
	TierEssential = "essential"
	TierFull      = "full"
)

var reTier = regexp.MustCompile(`(?m)^\s*--\s*tier:\s*([A-Za-z]+)\s*$`)

// ParseTier reads the `-- tier: essential` header of a query file. A file
// without one is full; an unknown value is an error, so a typo cannot make
// an essential collector silently full.
func ParseTier(sql string) (string, error) {
	m := reTier.FindStringSubmatch(sql)
	if m == nil {
		return TierFull, nil
	}
	switch m[1] {
	case TierEssential, TierFull:
		return m[1], nil
	}
	return "", fmt.Errorf("unknown tier %q (want %s or %s)", m[1], TierEssential, TierFull)
}

var reNeedsKeeper = regexp.MustCompile(`(?m)^\s*--\s*needs:\s*keeper\s*$`)

// NeedsKeeper reports whether a query file declares `-- needs: keeper`: it
// waits for a live Keeper session (system.zookeeper_connection,
// database_replicas, the Keeper-backed replicas columns), so a run on a
// server with no Keeper session skips it instead of waiting for its timeout.
func NeedsKeeper(sql string) bool { return reNeedsKeeper.MatchString(sql) }
