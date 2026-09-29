package secure

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

func configs(t *testing.T) (Config, Config) {
	t.Helper()
	apub, akey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bpub, bkey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := Config{Network: testutil.ID(1), Edge: testutil.ID(2), Local: testutil.ID(3), Peer: testutil.ID(4), Transport: model.UDP, Cipher: model.ChaCha20Poly1305, Identity: akey, PeerIdentity: bpub, Initiator: true}
	b := a
	b.Local, b.Peer = a.Peer, a.Local
	b.Identity = bkey
	b.PeerIdentity = apub
	b.Initiator = false
	return a, b
}
func establish(t *testing.T, a, b Config) (*Session, *Session) {
	t.Helper()
	initiator, err := NewHandshake(a)
	if err != nil {
		t.Fatal(err)
	}
	responder, err := NewHandshake(b)
	if err != nil {
		t.Fatal(err)
	}
	message, session, err := initiator.Write()
	if err != nil || session != nil {
		t.Fatal("first step", err)
	}
	if session, err := responder.Read(message); err != nil || session != nil {
		t.Fatal("first read", err)
	}
	message, session, err = responder.Write()
	if err != nil || session != nil {
		t.Fatal("second step", err)
	}
	if session, err := initiator.Read(message); err != nil || session != nil {
		t.Fatal("second read", err)
	}
	message, as, err := initiator.Write()
	if err != nil || as == nil {
		t.Fatal("final write", err)
	}
	bs, err := responder.Read(message)
	if err != nil || bs == nil {
		t.Fatal("final read", err)
	}
	if as.ID() != bs.ID() {
		t.Fatal("session binding mismatch")
	}
	return as, bs
}
func TestAuthenticatedSessions(t *testing.T) {
	a, b := configs(t)
	as, bs := establish(t, a, b)
	payload := []byte("overlay packet")
	encrypted, err := as.Seal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, payload) {
		t.Fatal("plaintext exposed")
	}
	plaintext, err := bs.Open(encrypted)
	if err != nil || !bytes.Equal(plaintext, payload) {
		t.Fatal("decrypt", err)
	}
	reverse, err := bs.Seal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(reverse, encrypted) {
		t.Fatal("directional keys were reused")
	}
	if _, err := as.Open(reverse); err != nil {
		t.Fatal(err)
	}
	freshA, freshB := establish(t, a, b)
	if freshA.ID() == as.ID() {
		t.Fatal("reused session binding")
	}
	if _, err := freshB.Open(encrypted); err == nil {
		t.Fatal("cross-session packet accepted")
	}
	if _, err := as.Open(encrypted); err == nil {
		t.Fatal("reflected packet accepted")
	}
}
func TestHandshakeBindsIdentityTopologyAndTransport(t *testing.T) {
	for _, kind := range []string{"identity", "network", "edge", "node", "transport"} {
		t.Run(kind, func(t *testing.T) {
			a, b := configs(t)
			switch kind {
			case "identity":
				a.PeerIdentity = make([]byte, ed25519.PublicKeySize)
			case "network":
				b.Network = testutil.ID(99)
			case "edge":
				b.Edge = testutil.ID(99)
			case "node":
				b.Peer = testutil.ID(99)
			case "transport":
				b.Transport = model.TCP
			}
			initiator, err := NewHandshake(a)
			if err != nil {
				t.Fatal(err)
			}
			responder, err := NewHandshake(b)
			if err != nil {
				t.Fatal(err)
			}
			first, _, err := initiator.Write()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := responder.Read(first); err != nil {
				return
			}
			second, _, err := responder.Write()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := initiator.Read(second); err == nil {
				t.Fatal("mismatched peer accepted")
			}
			if _, _, err := initiator.Write(); err == nil {
				t.Fatal("failed handshake remained usable")
			}
		})
	}
}
func TestInitiatorIdentityVerified(t *testing.T) {
	a, b := configs(t)
	b.PeerIdentity = make([]byte, ed25519.PublicKeySize)
	initiator, _ := NewHandshake(a)
	responder, _ := NewHandshake(b)
	first, _, _ := initiator.Write()
	responder.Read(first)
	second, _, _ := responder.Write()
	if _, err := initiator.Read(second); err != nil {
		t.Fatal(err)
	}
	third, _, err := initiator.Write()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.Read(third); err == nil {
		t.Fatal("unauthorized initiator accepted")
	}
}
func TestReplayWindowAndForgery(t *testing.T) {
	a, b := configs(t)
	as, bs := establish(t, a, b)
	frames := make([][]byte, ReplayWindow+3)
	for i := range frames {
		var err error
		frames[i], err = as.Seal([]byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Reordering inside the window is valid.
	for _, i := range []int{10, 0, 5} {
		if _, err := bs.Open(frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bs.Open(frames[5]); !errors.Is(err, ErrReplay) {
		t.Fatal("duplicate accepted")
	}
	forged := bytes.Clone(frames[11])
	binary.BigEndian.PutUint64(forged, MaxMessages-1)
	if _, err := bs.Open(forged); err == nil {
		t.Fatal("forged nonce accepted")
	}
	if _, err := bs.Open(frames[11]); err != nil {
		t.Fatal("forgery advanced window", err)
	}
	tampered := bytes.Clone(frames[12])
	tampered[len(tampered)-1] ^= 1
	if _, err := bs.Open(tampered); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := bs.Open(frames[12]); err != nil {
		t.Fatal("bad authentication consumed nonce", err)
	}
	if _, err := bs.Open(frames[len(frames)-1]); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Open(frames[1]); !errors.Is(err, ErrReplay) {
		t.Fatal("old packet accepted")
	}
}
func TestConcurrentNonceAllocation(t *testing.T) {
	a, b := configs(t)
	as, bs := establish(t, a, b)
	frames := make(chan []byte, 256)
	var wg sync.WaitGroup
	for range 256 {
		wg.Go(func() {
			frame, err := as.Seal([]byte("data"))
			if err != nil {
				t.Error(err)
				return
			}
			frames <- frame
		})
	}
	wg.Wait()
	close(frames)
	seen := map[uint64]bool{}
	for frame := range frames {
		nonce := binary.BigEndian.Uint64(frame)
		if seen[nonce] {
			t.Fatal("reused nonce")
		}
		seen[nonce] = true
		if _, err := bs.Open(frame); err != nil {
			t.Fatal(err)
		}
	}
}
func TestSessionLimits(t *testing.T) {
	a, b := configs(t)
	as, bs := establish(t, a, b)
	as.sequence = MaxMessages - 1
	if !as.NeedsRekey() {
		t.Fatal("nonce exhaustion not detected")
	}
	if _, err := as.Seal([]byte("data")); !errors.Is(err, ErrRekey) {
		t.Fatal(err)
	}
	bs.created = time.Now().Add(-MaxSessionAge)
	if !bs.NeedsRekey() {
		t.Fatal("age exhaustion not detected")
	}
}
func FuzzHandshakeInput(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 32))
	f.Add(make([]byte, 96))
	_, a, _ := ed25519.GenerateKey(rand.Reader)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	config := Config{Network: testutil.ID(1), Edge: testutil.ID(2), Local: testutil.ID(3), Peer: testutil.ID(4), Transport: model.UDP, Cipher: model.ChaCha20Poly1305, Identity: a, PeerIdentity: pub}
	f.Fuzz(func(t *testing.T, message []byte) {
		h, err := NewHandshake(config)
		if err != nil {
			t.Fatal(err)
		}
		h.Read(message)
	})
}

func TestSealAppendPrefixAndReuse(t *testing.T) {
	a, b := configs(t)
	sender, receiver := establish(t, a, b)
	buffer := make([]byte, 0, 4096)
	for i := range 64 {
		prefix := []byte{7, 8, 9}
		buffer = append(buffer[:0], prefix...)
		plaintext := bytes.Repeat([]byte{byte(i)}, 1200)
		encrypted, err := sender.SealAppend(buffer, plaintext)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encrypted[:3], prefix) {
			t.Fatal("prefix overwritten")
		}
		decoded, err := receiver.Open(encrypted[3:])
		if err != nil || !bytes.Equal(decoded, plaintext) {
			t.Fatalf("decrypt: %v", err)
		}
		if _, err := receiver.Open(encrypted[3:]); !errors.Is(err, ErrReplay) {
			t.Fatal("replay accepted", err)
		}
	}
}

func TestOpenInPlaceAuthenticationAndReplay(t *testing.T) {
	a, b := configs(t)
	sender, receiver := establish(t, a, b)
	plaintext := bytes.Repeat([]byte("secure"), 200)
	frame, err := sender.Seal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(frame)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := receiver.OpenInPlace(corrupt); err == nil {
		t.Fatal("forgery accepted")
	}
	original := bytes.Clone(frame)
	got, err := receiver.OpenInPlace(frame)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatal("valid frame failed after forgery", err)
	}
	if &got[0] != &frame[8] {
		t.Fatal("decryption allocated plaintext")
	}
	if _, err := receiver.OpenInPlace(original); !errors.Is(err, ErrReplay) {
		t.Fatal("replay accepted", err)
	}
}

func TestBatchAuthenticationMatchesSequentialReplayWindow(t *testing.T) {
	for _, jump := range []bool{false, true} {
		t.Run(fmt.Sprint(jump), func(t *testing.T) {
			a, b := configs(t)
			sender, receiver := establish(t, a, b)
			reference := newSession(receiver.id, bytes.Clone(receiver.binding), receiver.send, receiver.receive)
			early, err := sender.Seal(bytes.Repeat([]byte{77}, 1280))
			if err != nil {
				t.Fatal(err)
			}
			if jump {
				sender.sequence = ReplayWindow + 2
			}
			frames := make([][]byte, 32)
			for i := range frames {
				frames[i], err = sender.Seal(bytes.Repeat([]byte{byte(i)}, 4096))
				if err != nil {
					t.Fatal(err)
				}
			}
			frames[3] = bytes.Clone(frames[0])                       // Duplicate inside one parallel batch.
			frames[5][len(frames[5])-1] ^= 1                         // Authentication failure.
			binary.BigEndian.PutUint64(frames[7][:8], MaxMessages-2) // Forged high nonce.
			frames[8] = []byte{1, 2, 3}
			frames[31] = early // Falls outside the window only after preceding commits.
			expected := make([][]byte, len(frames))
			wantErrors := make([]error, len(frames))
			for i, frame := range frames {
				expected[i], wantErrors[i] = reference.Open(bytes.Clone(frame))
			}
			gotErrors := receiver.OpenBatchInPlace(frames)
			for i := range frames {
				if (gotErrors[i] == nil) != (wantErrors[i] == nil) || errors.Is(gotErrors[i], ErrReplay) != errors.Is(wantErrors[i], ErrReplay) || !bytes.Equal(frames[i], expected[i]) {
					t.Fatalf("entry %d: batch %v sequential %v", i, gotErrors[i], wantErrors[i])
				}
			}
			if receiver.highest != reference.highest || receiver.seen != reference.seen {
				t.Fatal("parallel authentication changed replay commits")
			}
		})
	}
}

func TestConcurrentBatchDuplicateAcceptedOnce(t *testing.T) {
	a, b := configs(t)
	sender, receiver := establish(t, a, b)
	var batches [2][][]byte
	for range 32 {
		raw, err := sender.Seal(bytes.Repeat([]byte{1}, 4096))
		if err != nil {
			t.Fatal(err)
		}
		for i := range batches {
			batches[i] = append(batches[i], bytes.Clone(raw))
		}
	}
	results := make(chan []error, 2)
	for _, batch := range batches {
		go func() { results <- receiver.OpenBatchInPlace(batch) }()
	}
	accepted := 0
	for range 2 {
		for _, err := range <-results {
			if err == nil {
				accepted++
			} else if !errors.Is(err, ErrReplay) {
				t.Fatal(err)
			}
		}
	}
	if accepted != 32 {
		t.Fatal("duplicate batch accepted more than once", accepted)
	}
}
