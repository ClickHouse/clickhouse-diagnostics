//go:build !unix

package hostinfo

import "runtime"

func runtimeArch() string { return runtime.GOARCH }
func runtimeOS() string   { return runtime.GOOS }

// collectDisks has no portable implementation outside unix. Returning
// nothing (rather than failing) keeps the Windows build usable for the
// ClickHouse-side collection, which is the part that works remotely anyway.
func collectDisks() ([]DiskInfo, error) { return nil, nil }
