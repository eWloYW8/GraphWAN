package secure

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestSealBatchMatchesSequentialWire(t *testing.T) {
	for _, cipher := range supportedTestCiphers {
		for _, count := range []int{1, 32, 128} {
			t.Run(fmt.Sprintf("%s/%d", cipher, count), func(t *testing.T) {
				a, b := configs(t)
				a.Cipher, b.Cipher = cipher, cipher
				sender, receiver := establish(t, a, b)
				reference := newSession(sender.id, sender.binding, sender.send, sender.receive)
				plain, dst := make([][]byte, count), make([][]byte, count)
				for i := range plain {
					plain[i] = bytes.Repeat([]byte{byte(i)}, 1200+i)
					dst[i] = make([]byte, 1, 1+Overhead+len(plain[i]))
					dst[i][0] = 42
				}
				if err := sender.SealBatchAppend(dst, plain); err != nil {
					t.Fatal(err)
				}
				for i, frame := range dst {
					want, err := reference.SealAppend([]byte{42}, plain[i])
					if err != nil || !bytes.Equal(frame, want) {
						t.Fatalf("wire mismatch at %d: %v", i, err)
					}
					got, err := receiver.Open(frame[1:])
					if err != nil || !bytes.Equal(got, plain[i]) {
						t.Fatalf("plaintext mismatch at %d: %v", i, err)
					}
					if _, err := receiver.Open(frame[1:]); !errors.Is(err, ErrReplay) {
						t.Fatal("replay accepted", err)
					}
				}
			})
		}
	}
}

func TestSealBatchRejectsBeforeMutation(t *testing.T) {
	a, b := configs(t)
	sender, _ := establish(t, a, b)
	for _, sequence := range []uint64{0, MaxMessages - 2, MaxMessages - 1, MaxMessages} {
		sender.sequence = sequence
		dst := [][]byte{{42}, {43}}
		plain := [][]byte{{1}, {2}}
		if sequence == 0 {
			plain[1] = nil
		}
		if err := sender.SealBatchAppend(dst, plain); err == nil {
			t.Fatal("accepted invalid batch")
		}
		if sender.sequence != sequence || !bytes.Equal(dst[0], []byte{42}) || !bytes.Equal(dst[1], []byte{43}) {
			t.Fatal("rejected batch mutated output or sequence")
		}
	}
}

func TestSealBatchConcurrentNonceReservation(t *testing.T) {
	a, b := configs(t)
	sender, _ := establish(t, a, b)
	frames := make(chan []byte, 320)
	var wg sync.WaitGroup
	for worker := range 10 {
		wg.Go(func() {
			if worker%2 == 0 {
				plain, dst := make([][]byte, 32), make([][]byte, 32)
				for i := range plain {
					plain[i] = make([]byte, 1400)
				}
				if err := sender.SealBatchAppend(dst, plain); err != nil {
					t.Error(err)
					return
				}
				for _, frame := range dst {
					frames <- frame
				}
			} else {
				for range 32 {
					frame, err := sender.Seal([]byte{1})
					if err != nil {
						t.Error(err)
						return
					}
					frames <- frame
				}
			}
		})
	}
	wg.Wait()
	close(frames)
	seen := map[uint64]bool{}
	for frame := range frames {
		nonce := binary.BigEndian.Uint64(frame)
		if seen[nonce] {
			t.Fatal("nonce reused", nonce)
		}
		seen[nonce] = true
	}
	for nonce := uint64(1); nonce <= 320; nonce++ {
		if !seen[nonce] {
			t.Fatal("missing nonce", nonce)
		}
	}
}
