package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/flynn/noise"
	"golang.org/x/crypto/chacha20poly1305"
)

func cipherFunction(suite model.CipherSuite) noise.CipherFunc {
	switch suite {
	case model.ChaCha20Poly1305:
		return noise.CipherChaChaPoly // Preserve existing v1 handshakes and messages.
	case model.AES256GCM:
		return noise.CipherAESGCM
	case model.AES128GCM, model.XChaCha20Poly1305:
		return extendedCipher{suite}
	default:
		panic("unvalidated cipher suite")
	}
}

// AES128GCM and XChaChaPoly are GraphWAN extensions to Noise's CipherFunc.
// Distinct names domain-separate their handshake transcripts and derived keys.
type extendedCipher struct{ suite model.CipherSuite }

func (c extendedCipher) CipherName() string {
	if c.suite == model.AES128GCM {
		return "AES128GCM"
	}
	return "XChaChaPoly"
}
func (c extendedCipher) Cipher(key [32]byte) noise.Cipher {
	var aead cipher.AEAD
	var err error
	if c.suite == model.AES128GCM {
		// Noise derives 32 key bytes; this suite uses the first 16 as its AES key.
		var block cipher.Block
		block, err = aes.NewCipher(key[:16])
		if err == nil {
			aead, err = cipher.NewGCM(block)
		}
	} else {
		aead, err = chacha20poly1305.NewX(key[:])
	}
	if err != nil {
		panic(err)
	} // All key and nonce sizes are fixed above.
	return counterCipher{aead: aead, extendedNonce: c.suite == model.XChaCha20Poly1305}
}

type counterCipher struct {
	aead          cipher.AEAD
	extendedNonce bool
}

func (c counterCipher) nonce(sequence uint64) []byte {
	if c.extendedNonce {
		var nonce [chacha20poly1305.NonceSizeX]byte
		binary.LittleEndian.PutUint64(nonce[16:], sequence)
		return nonce[:]
	}
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], sequence)
	return nonce[:]
}
func (c counterCipher) Encrypt(dst []byte, sequence uint64, ad, plaintext []byte) []byte {
	return c.aead.Seal(dst, c.nonce(sequence), plaintext, ad)
}
func (c counterCipher) Decrypt(dst []byte, sequence uint64, ad, ciphertext []byte) ([]byte, error) {
	return c.aead.Open(dst, c.nonce(sequence), ciphertext, ad)
}
