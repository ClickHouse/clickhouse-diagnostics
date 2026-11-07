//go:build !unix

package logfiles

import "os"

// openNoFollow：此平台没有 O_NOFOLLOW；Collect 中的目录扫描检查
// （Type().IsRegular()）以及 copyTail 中的 fstat 检查仍然保留。
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
