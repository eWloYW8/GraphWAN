//go:build !linux && !darwin && !windows

package main

import (
	"context"
	"errors"
	"github.com/eWloYW8/GraphWAN/internal/agent"
)

func agentUpdater(context.Context, string) (agent.Updater, error) { return nil, nil }
func runAgentUpdate([]string) error {
	return errors.New("managed updates require Linux, Windows or macOS")
}
func runUpdateHelper([]string) error { return errors.New("unsupported update helper platform") }
