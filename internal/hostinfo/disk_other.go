//go:build !unix

package hostinfo

import "runtime"

func runtimeArch() string { return runtime.GOARCH }
func runtimeOS() string   { return runtime.GOOS }

// collectDisks 在 unix 之外没有可移植实现。返回空结果（而不是报错）
// 可以让 Windows 构建仍能用于 ClickHouse 侧的采集，
// 反正那部分本来就是远程工作的。
func collectDisks() ([]DiskInfo, error) { return nil, nil }
