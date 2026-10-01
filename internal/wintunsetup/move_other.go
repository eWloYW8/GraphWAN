//go:build !windows

package wintunsetup

import "os"

// Used by portable archive-verification tests; production setup is Windows-only.
func moveFile(from, to string, replace bool) error {
	if replace {
		return os.Rename(from, to)
	}
	return os.Link(from, to)
}
