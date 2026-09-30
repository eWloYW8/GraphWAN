// Package store persists complete validated desired-state transactions in bbolt.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	bolt "go.etcd.io/bbolt"
)

var ErrConflict = errors.New("configuration revision conflict")
var bucket = []byte("graphwan")
var stateKey = []byte("state")

type Store struct {
	db        *bolt.DB
	committer atomic.Pointer[committer]
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucket)
		if err != nil {
			return err
		}
		if b.Get(stateKey) != nil {
			return nil
		}
		raw, err := json.Marshal(model.EmptyState())
		if err != nil {
			return err
		}
		return b.Put(stateKey, raw)
	})
	if err == nil {
		_, err = s.Read()
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open state: %w", err)
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }

func read(tx *bolt.Tx) (model.State, error) {
	var state model.State
	b := tx.Bucket(bucket)
	if b == nil {
		return state, errors.New("missing state bucket")
	}
	raw := b.Get(stateKey)
	if raw == nil {
		return state, errors.New("missing state record")
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if err := state.Validate(); err != nil {
		return state, err
	}
	return state, nil
}
func (s *Store) Read() (model.State, error) {
	var state model.State
	err := s.db.View(func(tx *bolt.Tx) error { var err error; state, err = read(tx); return err })
	return state, err
}

// Update serializes edits and commits state and revision atomically. The callback
// must not call Store methods. Failed validation/callback/commit leaves old state.
func (s *Store) Update(expected uint64, change func(*model.State) error) (model.State, error) {
	return s.UpdateWithRecords(expected, func(state *model.State, _ *Records) error { return change(state) })
}

// UpdateWithRecords atomically commits configuration and related security records.
func (s *Store) UpdateWithRecords(expected uint64, change func(*model.State, *Records) error) (model.State, error) {
	var result model.State
	var command Command
	err := s.db.View(func(tx *bolt.Tx) error {
		state, err := read(tx)
		if err != nil {
			return err
		}
		if state.Revision != expected {
			return ErrConflict
		}
		if expected == math.MaxUint64 {
			return errors.New("revision exhausted")
		}
		command = Command{Expected: version(tx), Records: make(map[string]*[]byte)}
		if err := change(&state, &Records{tx: tx, pending: command.Records}); err != nil {
			return err
		}
		state.Revision = expected + 1
		if err := state.Validate(); err != nil {
			return err
		}
		command.State = &state
		result = state.Clone()
		return nil
	})
	if err != nil {
		return model.State{}, err
	}
	if err := s.commit(command); err != nil {
		return model.State{}, err
	}
	return result, nil
}
