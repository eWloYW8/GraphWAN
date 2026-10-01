//go:build !linux && !darwin && !windows

package main

import "errors"

func runManagedService(string, []string) error {
	return errors.New("service management is supported on Linux (systemd, OpenRC or OpenWrt procd), Windows and macOS; use the foreground command on this platform")
}
