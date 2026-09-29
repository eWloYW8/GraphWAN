//go:build windows

package mesh

import (
	"errors"

	"golang.org/x/sys/windows"
)

func addressInUse(err error) bool { return errors.Is(err, windows.WSAEADDRINUSE) }
