//go:build !linux && !darwin && !windows

package gateway

import (
	"context"
	"errors"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

func applyPlatform(_ context.Context, _ model.ID, entries []Entry) error {
	if automatic(entries) {
		return errors.New("automatic gateway configuration is unsupported on this platform; use off mode and configure forwarding manually")
	}
	return nil
}
