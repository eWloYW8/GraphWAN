package secure

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"golang.org/x/crypto/chacha20poly1305"
)

var supportedTestCiphers = []model.CipherSuite{model.AES128GCM, model.AES256GCM, model.ChaCha20Poly1305, model.XChaCha20Poly1305}

func TestCipherMatchesStandardAEAD(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	for _, suite := range supportedTestCiphers {
		t.Run(string(suite), func(t *testing.T) {
			var reference cipher.AEAD
			var err error
			switch suite {
			case model.AES128GCM, model.AES256GCM:
				keyLen := 32
				if suite == model.AES128GCM {
					keyLen = 16
				}
				block, blockErr := aes.NewCipher(key[:keyLen])
				if blockErr != nil {
					t.Fatal(blockErr)
				}
				reference, err = cipher.NewGCM(block)
			case model.ChaCha20Poly1305:
				reference, err = chacha20poly1305.New(key[:])
			case model.XChaCha20Poly1305:
				reference, err = chacha20poly1305.NewX(key[:])
			}
			if err != nil {
				t.Fatal(err)
			}
			actual := cipherFunction(suite).Cipher(key)
			for _, sequence := range []uint64{0, 1, 255, 256, MaxMessages - 1, ^uint64(0)} {
				nonce := make([]byte, reference.NonceSize())
				if suite == model.AES128GCM || suite == model.AES256GCM {
					binary.BigEndian.PutUint64(nonce[len(nonce)-8:], sequence)
				} else {
					binary.LittleEndian.PutUint64(nonce[len(nonce)-8:], sequence)
				}
				plaintext, ad := []byte("test encrypted payload"), []byte("authenticated context")
				want := reference.Seal([]byte{42}, nonce, plaintext, ad)
				got := actual.Encrypt([]byte{42}, sequence, ad, plaintext)
				if !bytes.Equal(got, want) {
					t.Fatalf("cipher mismatch at nonce %d", sequence)
				}
				decoded, err := actual.Decrypt(nil, sequence, ad, got[1:])
				if err != nil || !bytes.Equal(decoded, plaintext) {
					t.Fatal("decrypt", err)
				}
				if _, err := actual.Decrypt(nil, sequence, []byte("wrong context"), got[1:]); err == nil {
					t.Fatal("accepted wrong associated data")
				}
			}
		})
	}
}

func TestCipherMismatchCannotEstablishSession(t *testing.T) {
	for _, left := range supportedTestCiphers {
		for _, right := range supportedTestCiphers {
			if left == right {
				continue
			}
			t.Run(string(left)+"/"+string(right), func(t *testing.T) {
				a, b := configs(t)
				a.Cipher, b.Cipher = left, right
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
					return
				}
				if _, err := initiator.Read(second); err == nil {
					t.Fatal("mismatched cipher authenticated")
				}
			})
		}
	}
}
