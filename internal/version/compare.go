package version

import "clickhouse-diagnostic/internal"

// Comparer 处理版本比较操作
type Comparer struct{}

// NewComparer 创建一个新的版本比较器
func NewComparer() *Comparer {
	return &Comparer{}
}

// IsGreater 比较版本
// 当版本 a > 版本 b 时返回 true
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

// IsEqual 当两个版本相等时返回 true
func (c *Comparer) IsEqual(a, b internal.Version) bool {
	return a.Major == b.Major &&
		a.Minor == b.Minor &&
		a.Patch == b.Patch &&
		a.Build == b.Build
}

// IsGreaterOrEqual 当版本 a >= 版本 b 时返回 true
func (c *Comparer) IsGreaterOrEqual(a, b internal.Version) bool {
	return c.IsGreater(a, b) || c.IsEqual(a, b)
}

// IsLess 当版本 a < 版本 b 时返回 true
func (c *Comparer) IsLess(a, b internal.Version) bool {
	return c.IsGreater(b, a)
}

// IsLessOrEqual 当版本 a <= 版本 b 时返回 true
func (c *Comparer) IsLessOrEqual(a, b internal.Version) bool {
	return c.IsLess(a, b) || c.IsEqual(a, b)
}
