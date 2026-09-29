// Package agent implements the durable Agent identity and controller client.
package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	bolt "go.etcd.io/bbolt"
)

var agentBucket = []byte("agent-v1")

const maxSnapshotBytes = 8 << 20

type Registration struct {
	AgentID     model.ID `json:"agent_id"`
	Server      string   `json:"server"`
	Certificate []byte   `json:"certificate"`
}

type Cache struct {
	db  *bolt.DB
	key ed25519.PrivateKey
}

func OpenCache(path string) (*Cache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	cache := &Cache{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(agentBucket)
		if err != nil {
			return err
		}
		key := b.Get([]byte("identity"))
		if key != nil {
			if len(key) != ed25519.PrivateKeySize {
				return errors.New("invalid stored private identity")
			}
			derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
			if !bytes.Equal(derived, key) {
				return errors.New("corrupted private identity")
			}
			cache.key = bytes.Clone(key)
			return nil
		}
		if b.Get([]byte("registration")) != nil || b.Get([]byte("desired")) != nil {
			return errors.New("agent identity is missing from existing cache")
		}
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		cache.key = bytes.Clone(key)
		return b.Put([]byte("identity"), key)
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return cache, nil
}
func (c *Cache) Close() error                   { return c.db.Close() }
func (c *Cache) PrivateKey() ed25519.PrivateKey { return bytes.Clone(c.key) }
func (c *Cache) PublicKey() ed25519.PublicKey   { return c.key.Public().(ed25519.PublicKey) }
func (c *Cache) CSR() ([]byte, error) {
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, c.key)
}

func (c *Cache) Registration() (*Registration, error) {
	var registration *Registration
	err := c.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(agentBucket).Get([]byte("registration"))
		if raw == nil {
			return nil
		}
		return json.Unmarshal(raw, &registration)
	})
	return registration, err
}
func (c *Cache) SaveRegistration(reg Registration) error {
	if err := reg.AgentID.Validate(); err != nil {
		return err
	}
	if _, err := serverURL(reg.Server); err != nil {
		return err
	}
	if _, err := c.TLSCertificate(reg); err != nil {
		return err
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(agentBucket)
		if prior := b.Get([]byte("registration")); prior != nil {
			var old Registration
			if err := json.Unmarshal(prior, &old); err != nil {
				return err
			}
			if old.AgentID != reg.AgentID || old.Server != reg.Server {
				return errors.New("registration cannot change agent or controller identity")
			}
		}
		return b.Put([]byte("registration"), raw)
	})
}
func (c *Cache) TLSCertificate(reg Registration) (tls.Certificate, error) {
	block, rest := pem.Decode(reg.Certificate)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return tls.Certificate{}, errors.New("invalid agent certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return tls.Certificate{}, err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !pub.Equal(c.PublicKey()) || cert.Subject.CommonName != string(reg.AgentID) {
		return tls.Certificate{}, errors.New("certificate does not match agent identity")
	}
	return tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: c.PrivateKey(), Leaf: cert}, nil
}

func snapshotFrom(b *bolt.Bucket, key string) (*model.Snapshot, error) {
	raw := b.Get([]byte(key))
	if raw == nil {
		return nil, nil
	}
	if len(raw) > maxSnapshotBytes {
		return nil, errors.New("cached snapshot too large")
	}
	var snapshot model.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	var reg Registration
	if err := json.Unmarshal(b.Get([]byte("registration")), &reg); err != nil {
		return nil, err
	}
	if err := snapshot.Validate(reg.AgentID); err != nil {
		return nil, fmt.Errorf("cached %s: %w", key, err)
	}
	return &snapshot, nil
}
func (c *Cache) Snapshots() (desired, applied *model.Snapshot, err error) {
	err = c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(agentBucket)
		var err error
		desired, err = snapshotFrom(b, "desired")
		if err != nil {
			return err
		}
		applied, err = snapshotFrom(b, "applied")
		return err
	})
	return
}

// SaveDesired must finish before runtime application. A revision identifies one
// immutable snapshot; replaying it with different content is an error.
func (c *Cache) SaveDesired(snapshot model.Snapshot) error {
	return c.saveSnapshot("desired", snapshot)
}
func (c *Cache) MarkApplied(snapshot model.Snapshot) error {
	return c.saveSnapshot("applied", snapshot)
}
func (c *Cache) saveSnapshot(key string, snapshot model.Snapshot) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(raw) > maxSnapshotBytes {
		return errors.New("snapshot too large")
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(agentBucket)
		var reg Registration
		if err := json.Unmarshal(b.Get([]byte("registration")), &reg); err != nil {
			return err
		}
		if err := snapshot.Validate(reg.AgentID); err != nil {
			return err
		}
		prior, err := snapshotFrom(b, key)
		if err != nil {
			return err
		}
		if prior != nil {
			if snapshot.Revision < prior.Revision {
				return errors.New("snapshot revision rollback rejected")
			}
			if snapshot.Revision == prior.Revision {
				previous, _ := json.Marshal(prior)
				if !bytes.Equal(raw, previous) {
					return errors.New("snapshot revision changed content")
				}
				return nil
			}
		}
		if key == "applied" {
			desired, err := snapshotFrom(b, "desired")
			if err != nil {
				return err
			}
			if desired == nil {
				return errors.New("cannot apply before persisting desired snapshot")
			}
			expected, _ := json.Marshal(desired)
			if !bytes.Equal(raw, expected) {
				return errors.New("applied snapshot differs from desired state")
			}
		}
		return b.Put([]byte(key), raw)
	})
}
