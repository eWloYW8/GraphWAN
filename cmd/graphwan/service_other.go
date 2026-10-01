//go:build !linux && !darwin && !windows

package main

import "errors"

func runManagedService(string, []string) error {
	return errors.New("service management is supported on Linux (systemd), Windows and macOS; use the foreground command on this platform")
}
