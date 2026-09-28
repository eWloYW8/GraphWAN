package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// ID is a random 128-bit identifier encoded as 32 lowercase hexadecimal digits.
// Zero is reserved for invalid/missing identifiers.
type ID string

func NewID() ID {
	var b [16]byte
	rand.Read(b[:])
	return ID(hex.EncodeToString(b[:]))
}

func (id ID) Validate() error {
	if len(id) != 32 {
		return fmt.Errorf("invalid ID %q: want 32 lowercase hex digits", id)
	}
	var nonzero bool
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("invalid ID %q", id)
		}
		nonzero = nonzero || c != '0'
	}
	if !nonzero {
		return fmt.Errorf("zero ID is reserved")
	}
	return nil
}
