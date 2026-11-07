package query

import (
	"fmt"
	"regexp"
)

// collector 的 tier（分层）。essential 采集器是开销低、能判定大多数故障的那些：
// 它们在每次采集中最先运行，也是 essential 采集唯一运行的采集器
// （见 internal/collectmode）。
const (
	TierEssential = "essential"
	TierFull      = "full"
)

var reTier = regexp.MustCompile(`(?m)^\s*--\s*tier:\s*([A-Za-z]+)\s*$`)

// ParseTier 读取查询文件头部的 `-- tier: essential` 标记。没有该标记的文件为
// full；未知取值会报错，因此拼写错误不会让 essential 采集器悄悄变成 full。
func ParseTier(sql string) (string, error) {
	m := reTier.FindStringSubmatch(sql)
	if m == nil {
		return TierFull, nil
	}
	switch m[1] {
	case TierEssential, TierFull:
		return m[1], nil
	}
	return "", fmt.Errorf("未知 tier %q（应为 %s 或 %s）", m[1], TierEssential, TierFull)
}

var reNeedsKeeper = regexp.MustCompile(`(?m)^\s*--\s*needs:\s*keeper\s*$`)

// NeedsKeeper 报告查询文件是否声明了 `-- needs: keeper`：该文件需要等待
// 存活的 Keeper 会话（system.zookeeper_connection、database_replicas 以及由
// Keeper 支撑的 replicas 相关列），因此在没有 Keeper 会话的服务器上运行时会跳过它，
// 而不是一直等到超时。
func NeedsKeeper(sql string) bool { return reNeedsKeeper.MatchString(sql) }
