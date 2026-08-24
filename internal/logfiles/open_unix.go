//go:build unix

package logfiles

import (
	"os"
	"syscall"
)

// openNoFollow opens path refusing to traverse a symlink at the final
// component — the fd-level counterpart of the directory-scan check in
// Collect, closing the scan-to-open race window.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
