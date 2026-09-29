package packetbuf

import "testing"

func TestSizeClassesAndExclusiveOwnership(t *testing.T) {
	held := make([]*Buffer, 256)
	for i := range held {
		size := 1280
		if i%2 == 1 {
			size = 9000
		}
		held[i] = Get(size)
		held[i].Data[0] = byte(i)
	}
	for i, b := range held {
		wantCap := 2048
		if i%2 == 1 {
			wantCap = 16384
		}
		if b.Data[0] != byte(i) || cap(b.Data) != wantCap {
			t.Fatal("simultaneous packets alias or size class is wrong")
		}
		b.Data = b.Data[80:100] // Simulate framing/decryption views.
		b.Release()
	}
	for _, size := range []int{0, 1280, 9000, 20000} {
		b := Get(size)
		if len(b.Data) != size {
			t.Fatal("wrong size after reuse")
		}
		if size > 16384 && b.pool != nil {
			t.Fatal("jumbo allocation retained in pool")
		}
		b.Release()
	}
}
func BenchmarkPacketBufferReuse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		buffer := Get(1280)
		buffer.Data[0] = 1
		buffer.Release()
	}
}
