package resources

import "golang.org/x/sys/windows"

func processCPUSeconds() (float64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	// CPU FILETIMEs are durations in 100 ns units, not dates since the epoch.
	seconds := func(value windows.Filetime) float64 {
		return float64(uint64(value.HighDateTime)<<32|uint64(value.LowDateTime)) / 1e7
	}
	return seconds(kernel) + seconds(user), nil
}
