package version

import "clickhouse-diagnostic/internal"

// Comparer handles version comparison operations
type Comparer struct{}

// NewComparer creates a new version comparer
func NewComparer() *Comparer {
	return &Comparer{}
}

// IsGreater compares versions
// Returns true if version a > version b
func (c *Comparer) IsGreater(a, b internal.Version) bool {
	if a.Major != b.Major {
		return a.Major > b.Major
	}
	if a.Minor != b.Minor {
		return a.Minor > b.Minor
	}
	if a.Patch != b.Patch {
		return a.Patch > b.Patch
	}
	return a.Build > b.Build
}

// IsEqual returns true if versions are equal
func (c *Comparer) IsEqual(a, b internal.Version) bool {
	return a.Major == b.Major &&
		a.Minor == b.Minor &&
		a.Patch == b.Patch &&
		a.Build == b.Build
}

// IsGreaterOrEqual returns true if version a >= version b
func (c *Comparer) IsGreaterOrEqual(a, b internal.Version) bool {
	return c.IsGreater(a, b) || c.IsEqual(a, b)
}

// IsLess returns true if version a < version b
func (c *Comparer) IsLess(a, b internal.Version) bool {
	return c.IsGreater(b, a)
}

// IsLessOrEqual returns true if version a <= version b
func (c *Comparer) IsLessOrEqual(a, b internal.Version) bool {
	return c.IsLess(a, b) || c.IsEqual(a, b)
}
