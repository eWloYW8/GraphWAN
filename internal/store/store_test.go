package store_test

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestDurabilityAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	got, err := db.Update(0, func(s *model.State) error { *s = testutil.Topology(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 {
		t.Fatal("callback was allowed to override revision")
	}
	_, err = db.Update(1, func(s *model.State) error { s.Networks[0].Edges[0].Weight = 0; return nil })
	if err == nil {
		t.Fatal("committed invalid configuration")
	}
	sentinel := errors.New("failed change")
	_, err = db.Update(1, func(s *model.State) error { s.Networks = nil; return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := db.Update(0, func(s *model.State) error { return nil }); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 || len(state.Networks) != 1 || state.Networks[0].Edges[0].Weight != 10 {
		t.Fatalf("rollback/restart lost state: %+v", state)
	}
}
func TestConcurrentEditors(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			_, err := db.Update(0, func(s *model.State) error { return nil })
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, store.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("successful edits=%d", winners.Load())
	}
}
