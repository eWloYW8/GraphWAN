//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !aix && !solaris && !windows

package resources

import "errors"

func processCPUSeconds() (float64, error) { return 0, errors.New("process CPU sampling unavailable") }
