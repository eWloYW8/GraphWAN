package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

func TestPreparedTransactionAtomicCASAndLocalIsolation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.PutLocal("identity", []byte("local"))
	var prepared store.Command
	db.SetCommitter(func(c store.Command) error { prepared = c; return store.ErrUnavailable })
	_, err = db.UpdateWithRecords(0, func(s *model.State, r *store.Records) error { return r.Put("token", []byte("consumed")) })
	if !errors.Is(err, store.ErrUnavailable) {
		t.Fatal(err)
	}
	image, _ := db.Export()
	if image.Version != 0 || image.State.Revision != 0 || len(image.Records) != 0 {
		t.Fatal("rejected command leaked changes")
	}
	if err = db.Apply(prepared); err != nil {
		t.Fatal(err)
	}
	if err = db.Apply(prepared); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate transaction: %v", err)
	}
	image, _ = db.Export()
	if image.Version != 1 || image.State.Revision != 1 || string(image.Records["token"]) != "consumed" {
		t.Fatal("incomplete atomic apply")
	}
	peer, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.PutLocal("identity", []byte("peer"))
	if err = peer.Import(image); err != nil {
		t.Fatal(err)
	}
	local, _ := peer.Local("identity")
	if string(local) != "peer" {
		t.Fatal("replication replaced local identity")
	}
	db.SetCommitter(nil)
	v, _ := db.Version()
	if err = db.WriteRecords(func(r *store.Records) error { r.Get("token"); return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := db.Version()
	if after != v {
		t.Fatal("read-only security access created a transaction")
	}
}
