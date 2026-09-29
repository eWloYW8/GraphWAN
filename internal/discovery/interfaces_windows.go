//go:build windows

package discovery

import (
	"net"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func physicalInterfaces(_ []net.Interface) (map[int]bool, error) {
	rows, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormal)
	if err != nil {
		return nil, err
	}
	result := make(map[int]bool, len(rows))
	for _, row := range rows {
		flags := row.InterfaceAndOperStatusFlags
		metadata := interfaceMetadata{kind: uint32(row.Type),
			hardware: flags&winipcfg.IAOSFHardwareInterface != 0,
			filter:   flags&winipcfg.IAOSFFilterInterface != 0,
			endpoint: flags&winipcfg.IAOSFEndPointInterface != 0,
		}
		result[int(row.InterfaceIndex)] = metadata.physical()
	}
	return result, nil
}
