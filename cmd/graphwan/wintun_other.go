//go:build !windows

package main

import (
	"context"
	"errors"
)

func prepareAgentPlatform(context.Context) error { return nil }
func runInstallWintun(context.Context, []string) error {
	return errors.New("install-wintun is available only on Windows")
}
