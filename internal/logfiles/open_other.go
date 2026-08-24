//go:build !unix

package logfiles

import "os"

// openNoFollow: no O_NOFOLLOW on this platform; the directory-scan check
// in Collect (Type().IsRegular()) and the fstat check in copyTail remain.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
