package transport

import (
	"bytes"
	"encoding/hex"
	"net/netip"
	"testing"
)

// Independent Windows ABI byte fixtures: SIZE_T + INT + INT, followed by
// IN_PKTINFO or IN6_PKTINFO. The last IPv6/64 object includes four pad bytes.
var winsockFixtures = []struct {
	word        int
	remote, raw string
}{
	{4, "192.0.2.2:24752", "14000000 00000000 13000000 c0000201 07000000"},
	{8, "192.0.2.2:24752", "1800000000000000 00000000 13000000 c0000201 07000000"},
	{4, "[fe80::2%7]:24752", "20000000 29000000 13000000 fe800000000000000000000000000001 07000000"},
	{8, "[fe80::2%7]:24752", "2400000000000000 29000000 13000000 fe800000000000000000000000000001 07000000 00000000"},
}

func hexBytes(raw string) []byte {
	data, err := hex.DecodeString(string(bytes.ReplaceAll([]byte(raw), []byte(" "), nil)))
	if err != nil {
		panic(err)
	}
	return data
}

func TestWinsockPacketInfoABI(t *testing.T) {
	for _, fixture := range winsockFixtures {
		raw := hexBytes(fixture.raw)
		remote := netip.MustParseAddrPort(fixture.remote)
		got := winsockReplyControl(raw, remote, fixture.word)
		if !bytes.Equal(got, raw) {
			t.Fatalf("ABI %d/%s: %x, want %x", fixture.word, remote, got, raw)
		}
		raw[fixture.word+8] ^= 1
		if bytes.Equal(got, raw) {
			t.Fatal("reply shares mutable read buffer storage")
		}
		if remote.Addr().Is4() {
			mapped := netip.MustParseAddrPort("[::ffff:192.0.2.2]:24752")
			if !bytes.Equal(winsockReplyControl(hexBytes(fixture.raw), mapped, fixture.word), got) {
				t.Fatal("mapped peer used IPv6 controls")
			}
		}
	}
}

func TestWinsockControlChainAndMalformedInput(t *testing.T) {
	for _, fixture := range winsockFixtures {
		raw := hexBytes(fixture.raw)
		remote := netip.MustParseAddrPort(fixture.remote)
		prefix := hexBytes("0d000000 00000000 02000000 40000000")
		if fixture.word == 8 {
			prefix = hexBytes("1100000000000000 00000000 02000000 4000000000000000")
		}
		if got := winsockReplyControl(append(prefix, raw...), remote, fixture.word); !bytes.Equal(got, raw) {
			t.Fatal("unrelated control object or alignment corrupted packet info")
		}
		for n := 0; n < fixture.word+8; n++ {
			if got := winsockReplyControl(raw[:n], remote, fixture.word); got != nil {
				t.Fatal("short header accepted")
			}
		}
		for _, offset := range []int{0, fixture.word + 4} {
			bad := bytes.Clone(raw)
			bad[offset] = 255
			if got := winsockReplyControl(bad, remote, fixture.word); got != nil {
				t.Fatal("invalid length/type accepted")
			}
		}
		bad := bytes.Clone(raw)
		clear(bad[fixture.word+8 : len(bad)-4])
		if got := winsockReplyControl(bad, remote, fixture.word); got != nil {
			t.Fatal("unspecified reply source accepted")
		}
		invalidSources := []string{"224.0.0.1"}
		if remote.Addr().Is6() {
			invalidSources = []string{"ff02::1", "::ffff:192.0.2.1"}
		}
		for _, source := range invalidSources {
			bad := bytes.Clone(raw)
			copy(bad[fixture.word+8:], netip.MustParseAddr(source).AsSlice())
			if got := winsockReplyControl(bad, remote, fixture.word); got != nil {
				t.Fatalf("invalid source accepted: %s", source)
			}
		}
		if got := winsockReplyControl(append(bytes.Clone(raw), raw...), remote, fixture.word); got != nil {
			t.Fatal("ambiguous duplicate packet information accepted")
		}
		if got := winsockReplyControl(raw, remote, 3); got != nil {
			t.Fatal("invalid ABI accepted")
		}
		if got := winsockReplyControl(raw, netip.AddrPort{}, fixture.word); got != nil {
			t.Fatal("invalid remote accepted")
		}
	}
}

func FuzzWinsockReplyControl(f *testing.F) {
	for _, fixture := range winsockFixtures {
		f.Add(hexBytes(fixture.raw), fixture.word == 8, fixture.remote)
	}
	f.Fuzz(func(t *testing.T, raw []byte, wide bool, remoteString string) {
		word := 4
		if wide {
			word = 8
		}
		remote, _ := netip.ParseAddrPort(remoteString)
		got := winsockReplyControl(raw, remote, word)
		if got != nil {
			if len(got) > 40 || len(got)%word != 0 {
				t.Fatal("unbounded or misaligned output")
			}
			if repeated := winsockReplyControl(got, remote, word); !bytes.Equal(got, repeated) {
				t.Fatal("emitted malformed packet control")
			}
		}
	})
}
