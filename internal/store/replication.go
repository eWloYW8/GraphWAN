package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"

	"github.com/eWloYW8/GraphWAN/internal/model"
	bolt "go.etcd.io/bbolt"
)

var versionKey = []byte("transaction-version")
var localBucket = []byte("server-local")
var ErrUnavailable = errors.New("configuration writes require an available cluster majority")

// Command is an optimistic transaction, including one-time security records.
// Raft orders commands; the FSM checks the version before changing any bytes.
type Command struct {
	Expected uint64             `json:"expected"`
	State    *model.State       `json:"state,omitempty"`
	Records  map[string]*[]byte `json:"records,omitempty"`
}
type committer struct{ apply func(Command) error }

func (s *Store) SetCommitter(apply func(Command) error) {
	if apply == nil {
		s.committer.Store(nil)
	} else {
		s.committer.Store(&committer{apply: apply})
	}
}
func (s *Store) commit(c Command) error {
	if apply := s.committer.Load(); apply != nil {
		return apply.apply(c)
	}
	return s.Apply(c)
}
func version(tx *bolt.Tx) uint64 {
	raw := tx.Bucket(bucket).Get(versionKey)
	if len(raw) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(raw)
}
func (s *Store) Version() (uint64, error) {
	var v uint64
	err := s.db.View(func(tx *bolt.Tx) error { v = version(tx); return nil })
	return v, err
}
func putVersion(tx *bolt.Tx, v uint64) error {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], v)
	return tx.Bucket(bucket).Put(versionKey, raw[:])
}

func (s *Store) Apply(c Command) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if version(tx) != c.Expected {
			return ErrConflict
		}
		if c.Expected == math.MaxUint64 {
			return errors.New("transaction version exhausted")
		}
		if c.State != nil {
			old, err := read(tx)
			if err != nil {
				return err
			}
			if old.Revision == math.MaxUint64 || c.State.Revision != old.Revision+1 {
				return ErrConflict
			}
			if err := c.State.Validate(); err != nil {
				return err
			}
			raw, err := json.Marshal(c.State)
			if err != nil {
				return err
			}
			if err := tx.Bucket(bucket).Put(stateKey, raw); err != nil {
				return err
			}
		}
		r := &Records{tx: tx}
		for key, value := range c.Records {
			var err error
			if value == nil {
				err = r.Delete(key)
			} else {
				err = r.Put(key, *value)
			}
			if err != nil {
				return err
			}
		}
		return putVersion(tx, c.Expected+1)
	})
}

// Image deliberately excludes local identities, discovery caches and Raft logs.
// It includes the cluster CA and password hash and must only cross authenticated
// cluster channels or an explicitly authorized, server-verified join exchange.
type Image struct {
	Version uint64            `json:"version"`
	State   model.State       `json:"state"`
	Records map[string][]byte `json:"records"`
}

func (s *Store) Export() (Image, error) {
	var image Image
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		image.State, err = read(tx)
		if err != nil {
			return err
		}
		image.Version = version(tx)
		image.Records = make(map[string][]byte)
		if b := tx.Bucket(recordsBucket); b != nil {
			return b.ForEach(func(k, v []byte) error { image.Records[string(k)] = bytes.Clone(v); return nil })
		}
		return nil
	})
	return image, err
}
func (s *Store) Import(image Image) error { return s.ImportWithLocal(image, nil) }

func (s *Store) ImportWithLocal(image Image, local map[string][]byte) error {
	if err := image.State.Validate(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(recordsBucket) != nil {
			if err := tx.DeleteBucket(recordsBucket); err != nil {
				return err
			}
		}
		r := &Records{tx: tx}
		for key, value := range image.Records {
			if err := r.Put(key, value); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(image.State)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bucket).Put(stateKey, raw); err != nil {
			return err
		}
		b, err := tx.CreateBucketIfNotExists(localBucket)
		if err != nil {
			return err
		}
		for key, value := range local {
			if value == nil {
				err = b.Delete([]byte(key))
			} else {
				err = b.Put([]byte(key), value)
			}
			if err != nil {
				return err
			}
		}
		return putVersion(tx, image.Version)
	})
}

func (s *Store) Local(key string) ([]byte, error) {
	var raw []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(localBucket); b != nil {
			raw = bytes.Clone(b.Get([]byte(key)))
		}
		return nil
	})
	return raw, err
}
func (s *Store) PutLocal(key string, value []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(localBucket)
		if err != nil {
			return err
		}
		if value == nil {
			return b.Delete([]byte(key))
		}
		return b.Put([]byte(key), value)
	})
}
