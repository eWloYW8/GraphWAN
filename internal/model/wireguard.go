package model

import (
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
)

func ValidateWireGuardKey(key string) error {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 || base64.StdEncoding.EncodeToString(raw) != key {
		return fmt.Errorf("WireGuard public key must be canonical base64 encoding of 32 bytes")
	}
	public, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return err
	}
	// Reject low-order points, which can never form a valid WireGuard session.
	secret := make([]byte, 32)
	secret[0] = 1
	private, _ := ecdh.X25519().NewPrivateKey(secret)
	if _, err = private.ECDH(public); err != nil {
		return fmt.Errorf("invalid WireGuard public key")
	}
	return nil
}
func (w WireGuardNode) Validate() error {
	if err := ValidateWireGuardKey(w.PublicKey); err != nil {
		return err
	}
	if w.Endpoint != "" {
		endpoint := Endpoint{ID: ID("00000000000000000000000000000001"), Transport: UDP, Source: Manual, URL: "udp://" + w.Endpoint}
		if err := endpoint.Validate(); err != nil {
			return fmt.Errorf("WireGuard endpoint: %w", err)
		}
	}
	return nil
}
