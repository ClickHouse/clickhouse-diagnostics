//go:build unix

package logfiles

import (
	"os"
	"syscall"
)

// openNoFollow 打开 path 时拒绝跟随最终组件上的符号链接——
// 这是 Collect 中目录扫描检查的 fd 级对应物，用于关闭扫描到打开之间的竞态窗口。
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
