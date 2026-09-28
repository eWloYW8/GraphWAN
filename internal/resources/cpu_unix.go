//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || aix || solaris

package resources

import "golang.org/x/sys/unix"

func processCPUSeconds() (float64, error) {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	return float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6 + float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6, nil
}
