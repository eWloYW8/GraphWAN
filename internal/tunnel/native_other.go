//go:build !linux && !freebsd && !darwin && !windows && !openbsd && !netbsd && !dragonfly

package tunnel

import (
	"fmt"
	"runtime"
)

func Open(config Config) (Device, error) {
	return nil, fmt.Errorf("native TUN adapter for %s is not implemented yet", runtime.GOOS)
}
