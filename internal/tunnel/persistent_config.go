package tunnel

import (
	"errors"
	"strconv"
	"strings"
)

// Persistent BSD TUN names are immutable driver/unit pairs. Limit our allocator
// to 16-bit units; the kernel and device-number encoding both accommodate them.
func persistentTunUnit(name string) (uint32, error) {
	if !strings.HasPrefix(name, "tun") {
		return 0, errors.New("BSD TUN name must be tun followed by a decimal index")
	}
	suffix := strings.TrimPrefix(name, "tun")
	unit, err := strconv.ParseUint(suffix, 10, 16)
	if err != nil || strconv.FormatUint(unit, 10) != suffix {
		return 0, errors.New("invalid BSD TUN index")
	}
	return uint32(unit), nil
}
