package packet

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"net/netip"
)

type IPInfo struct {
	Source, Destination netip.Addr
	Flow                uint64
}

// InspectIP validates base IP lengths and hashes a stable flow key. Fragmented
// packets use an address/protocol key so all fragments follow the same path.
// IPv6 extension chains use the base next-header key until ECMP is enabled.
func InspectIP(raw []byte) (IPInfo, error) {
	var info IPInfo
	if len(raw) < 1 {
		return info, errors.New("empty IP packet")
	}
	h := fnv.New64a()
	var protocol byte
	var offset int
	var fragmented bool
	switch raw[0] >> 4 {
	case 4:
		if len(raw) < 20 {
			return info, errors.New("truncated IPv4 header")
		}
		offset = int(raw[0]&15) * 4
		if offset < 20 || offset > len(raw) || int(binary.BigEndian.Uint16(raw[2:4])) != len(raw) {
			return info, errors.New("invalid IPv4 length")
		}
		info.Source = netip.AddrFrom4([4]byte(raw[12:16]))
		info.Destination = netip.AddrFrom4([4]byte(raw[16:20]))
		protocol = raw[9]
		fragmented = binary.BigEndian.Uint16(raw[6:8])&0x3fff != 0
		h.Write(raw[12:20])
	case 6:
		if len(raw) < 40 || int(binary.BigEndian.Uint16(raw[4:6]))+40 != len(raw) {
			return info, errors.New("invalid IPv6 length")
		}
		info.Source = netip.AddrFrom16([16]byte(raw[8:24]))
		info.Destination = netip.AddrFrom16([16]byte(raw[24:40]))
		protocol = raw[6]
		offset = 40
		h.Write(raw[8:40])
	default:
		return info, errors.New("unsupported IP version")
	}
	h.Write([]byte{protocol})
	if !fragmented && (protocol == 6 || protocol == 17) && len(raw) >= offset+4 {
		h.Write(raw[offset : offset+4])
	}
	info.Flow = h.Sum64()
	return info, nil
}
