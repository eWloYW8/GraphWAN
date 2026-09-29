// Package secure authenticates configured peers and encrypts packet sessions.
package secure

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/flynn/noise"
)

const MaxHandshake = 1024

type Config struct {
	Network      model.ID
	Edge         model.ID
	Local        model.ID
	Peer         model.ID
	Transport    model.Transport
	Cipher       model.CipherSuite
	Identity     ed25519.PrivateKey
	PeerIdentity ed25519.PublicKey
	Initiator    bool
}

type Handshake struct {
	state    *noise.HandshakeState
	config   Config
	prologue []byte
	proof    []byte
	failed   bool
	finished bool
}

// NewHandshake uses Noise XX with fresh X25519 static AND ephemeral keys for
// each connection. Ed25519 signatures bind the per-handshake static keys to the
// configured identities; no identity private key or session key is distributed.
func NewHandshake(config Config) (*Handshake, error) {
	for _, id := range []model.ID{config.Network, config.Edge, config.Local, config.Peer} {
		if err := id.Validate(); err != nil {
			return nil, err
		}
	}
	if config.Local == config.Peer || !config.Transport.Valid() || config.Cipher != model.ChaCha20Poly1305 {
		return nil, errors.New("invalid peer handshake policy")
	}
	if len(config.Identity) != ed25519.PrivateKeySize || len(config.PeerIdentity) != ed25519.PublicKeySize {
		return nil, errors.New("invalid handshake identities")
	}
	if !bytes.Equal(ed25519.NewKeyFromSeed(config.Identity.Seed()), config.Identity) {
		return nil, errors.New("invalid private identity")
	}
	initiator, responder := config.Local, config.Peer
	if !config.Initiator {
		initiator, responder = responder, initiator
	}
	prologue := []byte(fmt.Sprintf("GraphWAN/peer/v1/%s/%s/%s/%s/%s", config.Network, config.Edge, initiator, responder, config.Transport))
	static, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, err
	}
	state, err := noise.NewHandshakeState(noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s), Pattern: noise.HandshakeXX, Initiator: config.Initiator, Prologue: prologue, StaticKeypair: static})
	if err != nil {
		return nil, err
	}
	proof := ed25519.Sign(config.Identity, identityProof(prologue, config.Initiator, static.Public))
	// Keep a copy of the expected public key; the handshake does not need to
	// retain the long-lived private key after signing its temporary static key.
	config.PeerIdentity = bytes.Clone(config.PeerIdentity)
	config.Identity = nil
	return &Handshake{state: state, config: config, prologue: prologue, proof: proof}, nil
}
func identityProof(prologue []byte, initiator bool, static []byte) []byte {
	message := append([]byte("GraphWAN/identity/v1/"), prologue...)
	role := byte(0)
	if initiator {
		role = 1
	}
	message = append(message, role)
	return append(message, static...)
}

// Write returns a Noise handshake message and, at the final step, a Session.
// Callers must send the returned message before using that Session for traffic.
func (h *Handshake) Write() ([]byte, *Session, error) {
	if h.failed || h.finished {
		return nil, nil, errors.New("handshake is no longer active")
	}
	var payload []byte
	if h.state.MessageIndex() != 0 {
		payload = h.proof
	}
	message, first, second, err := h.state.WriteMessage(nil, payload)
	if err != nil {
		h.failed = true
		return nil, nil, err
	}
	return message, h.finish(first, second), nil
}
func (h *Handshake) Read(message []byte) (*Session, error) {
	if h.failed || h.finished {
		return nil, errors.New("handshake is no longer active")
	}
	if len(message) > MaxHandshake {
		h.failed = true
		return nil, errors.New("handshake message too large")
	}
	step := h.state.MessageIndex()
	payload, first, second, err := h.state.ReadMessage(nil, message)
	if err != nil {
		h.failed = true
		return nil, err
	}
	if step == 0 {
		if len(payload) != 0 {
			h.failed = true
			return nil, errors.New("unexpected initial handshake payload")
		}
	} else {
		proof := identityProof(h.prologue, !h.config.Initiator, h.state.PeerStatic())
		if !ed25519.Verify(h.config.PeerIdentity, proof, payload) {
			h.failed = true
			return nil, errors.New("peer identity authentication failed")
		}
	}
	return h.finish(first, second), nil
}
func (h *Handshake) finish(first, second *noise.CipherState) *Session {
	if first == nil || second == nil {
		return nil
	}
	h.finished = true
	send, receive := first, second
	if !h.config.Initiator {
		send, receive = receive, send
	}
	binding := bytes.Clone(h.state.ChannelBinding())
	return newSession(hex.EncodeToString(binding), binding, send.Cipher(), receive.Cipher())
}
