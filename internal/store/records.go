package store

import (
	"bytes"
	"errors"

	bolt "go.etcd.io/bbolt"
)

var recordsBucket = []byte("records")

// Records lives only for the duration of a transaction callback. Values returned
// by Get are copied and may safely outlive that callback.
type Records struct {
	tx      *bolt.Tx
	pending map[string]*[]byte
}

func (r *Records) Get(key string) []byte {
	if value, ok := r.pending[key]; ok {
		if value == nil {
			return nil
		}
		return bytes.Clone(*value)
	}
	b := r.tx.Bucket(recordsBucket)
	if b == nil {
		return nil
	}
	return bytes.Clone(b.Get([]byte(key)))
}
func (r *Records) Put(key string, value []byte) error {
	if key == "" || len(key) > 256 || len(value) > 1<<20 {
		return errors.New("invalid record size")
	}
	if r.pending != nil {
		copy := bytes.Clone(value)
		r.pending[key] = &copy
		return nil
	}
	b, err := r.tx.CreateBucketIfNotExists(recordsBucket)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), value)
}
func (r *Records) Delete(key string) error {
	if r.pending != nil {
		r.pending[key] = nil
		return nil
	}
	b := r.tx.Bucket(recordsBucket)
	if b == nil {
		return nil
	}
	return b.Delete([]byte(key))
}
func (s *Store) ReadRecords(fn func(*Records) error) error {
	return s.db.View(func(tx *bolt.Tx) error { return fn(&Records{tx: tx}) })
}
func (s *Store) WriteRecords(fn func(*Records) error) error {
	for range 32 {
		var command Command
		if err := s.db.View(func(tx *bolt.Tx) error {
			command = Command{Expected: version(tx), Records: make(map[string]*[]byte)}
			return fn(&Records{tx: tx, pending: command.Records})
		}); err != nil {
			return err
		}
		if len(command.Records) == 0 {
			return nil
		}
		err := s.commit(command)
		if errors.Is(err, ErrConflict) {
			continue
		}
		return err
	}
	return ErrConflict
}
