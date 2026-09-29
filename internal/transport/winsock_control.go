package transport

import (
	"encoding/binary"
	"net/netip"
)

// WSACMSGHDR is SIZE_T followed by two INTs. The data and each next header
// are aligned to SIZE_T; cmsg_len excludes trailing padding. Windows targets
// supported by Go are little-endian. Keep the codec portable for ABI tests.
func winsockReplyControl(raw []byte, remote netip.AddrPort, word int) []byte {
	if word != 4 && word != 8 || !remote.IsValid() {
		return nil
	}
	header := word + 8
	level, payload := uint32(41), 20 // IPPROTO_IPV6, sizeof(IN6_PKTINFO)
	if remote.Addr().Unmap().Is4() {
		level, payload = 0, 8 // IPPROTO_IP, sizeof(IN_PKTINFO)
	}
	var result []byte
	for len(raw) >= header {
		n := uint64(binary.LittleEndian.Uint32(raw))
		if word == 8 {
			n = binary.LittleEndian.Uint64(raw)
		}
		if n < uint64(header) || n > uint64(len(raw)) {
			return nil
		}
		if binary.LittleEndian.Uint32(raw[word:]) == level && binary.LittleEndian.Uint32(raw[word+4:]) == 19 { // IP[v6]_PKTINFO
			if n != uint64(header+payload) || result != nil {
				return nil
			}
			data := raw[header:int(n)]
			address, _ := netip.AddrFromSlice(data[:payload-4])
			if address.IsUnspecified() || address.IsMulticast() || address.Is4In6() {
				return nil
			}
			// Retain only the packet-info object, including its interface index.
			// Unrelated receive-only controls must not be passed to WSASendMsg.
			result = make([]byte, (int(n)+word-1)&^(word-1))
			copy(result, raw[:int(n)])
		}
		next := (int(n) + word - 1) & ^(word - 1)
		if next >= len(raw) {
			break
		}
		raw = raw[next:]
	}
	return result
}
