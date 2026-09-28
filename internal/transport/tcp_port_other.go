//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package transport

import "syscall"

// Other Go platforms retain direct TCP. Their native punching support remains
// outside the currently implemented platform adapters.
func reuseTCPPort(_, _ string, _ syscall.RawConn) error { return nil }
