package secure

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
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
	for _, suite := range supportedTestCiphers {
		t.Run(string(suite), func(t *testing.T) { runTestAuthenticatedSessions(t, suite) })
	}
}

func runTestAuthenticatedSessions(t *testing.T, suite model.CipherSuite) {
	a, b := configs(t)
	a.Cipher, b.Cipher = suite, suite
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
