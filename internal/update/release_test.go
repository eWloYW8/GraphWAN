package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func testAsset(body []byte) model.ReleaseAsset {
	digest := sha256.Sum256(body)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	name := "graphwan-v9.9.9-" + runtime.GOOS + "-" + runtime.GOARCH + suffix
	return model.ReleaseAsset{Version: "v9.9.9", OS: runtime.GOOS, Arch: runtime.GOARCH, Name: name, URL: "https://github.com/eWloYW8/GraphWAN/releases/download/v9.9.9/" + name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestVerifiedReleaseCache(t *testing.T) {
	for _, versions := range [][2]string{{"v1.2.3", "v1.2.3"}, {"v1.2.4", "v1.2.3"}, {"2.0.0", "v1.9.9"}} {
		if CheckVersion(versions[0], versions[1]) == nil {
			t.Fatal("equal/older release accepted", versions)
		}
	}
	if CheckVersion("v1.2.3", "v1.2.4") != nil || CheckVersion("dev", "v1.2.4") != nil {
		t.Fatal("valid upgrade rejected")
	}

	body := []byte("verified executable")
	a := testAsset(body)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "latest") {
			json.NewEncoder(w).Encode(map[string]any{"tag_name": a.Version, "assets": []any{map[string]any{"name": a.Name, "browser_download_url": a.URL, "digest": "sha256:" + a.SHA256, "size": a.Size}}})
			return
		}
		downloads.Add(1)
		w.Write(body)
	}))
	defer server.Close()
	client := server.Client()
	base := client.Transport
	client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		u := *r.URL
		copy.URL = &u
		copy.URL.Scheme = "http"
		copy.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return base.RoundTrip(copy)
	})
	s := New(t.TempDir())
	s.Client = client
	got, err := s.Latest(context.Background(), runtime.GOOS, runtime.GOARCH)
	if err != nil || got != a {
		t.Fatal(got, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			path, err := s.Cached(context.Background(), a)
			if err != nil {
				t.Error(err)
				return
			}
			if err = Verify(path, a); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if downloads.Load() != 1 {
		t.Fatal("duplicate cache downloads", downloads.Load())
	}
	path := filepath.Join(s.Directory, a.SHA256)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cached(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 2 {
		t.Fatal("corrupt cache was not replaced")
	}
	bad := a
	bad.SHA256 = strings.Repeat("0", 64)
	stage := filepath.Join(t.TempDir(), "bad")
	if Fetch(context.Background(), client, a.URL, stage, bad) == nil {
		t.Fatal("bad digest accepted")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("failed download left a staging file")
	}
	bad = a
	bad.URL = "https://example.com/evil"
	if bad.Validate() == nil {
		t.Fatal("foreign release accepted")
	}
}
func TestManagerDeduplicatesAndPreservesExecutable(t *testing.T) {
	body := []byte("new release")
	a := testAsset(body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer server.Close()
	dir := t.TempDir()
	executable := filepath.Join(dir, "graphwan")
	os.WriteFile(executable, []byte("old"), 0700)
	req := model.UpdateRequest{ID: model.NewID(), Asset: a, Source: "server", CreatedAt: time.Now()}
	installed := make(chan struct{}, 1)
	manager := NewManager(context.Background(), Receipt{Name: "test", Data: dir, Executable: executable}, "old", func(r Receipt, stage string, request model.UpdateRequest) error {
		defer os.Remove(stage)
		if err := Verify(stage, a); err != nil {
			return err
		}
		if request.ID != req.ID {
			return fmt.Errorf("wrong update request")
		}
		installed <- struct{}{}
		return nil
	})
	if err := manager.Start(req, server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-installed:
	case <-time.After(3 * time.Second):
		t.Fatal("update did not stage")
	}
	if err := manager.Start(req, server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(executable)
	if string(raw) != "old" {
		t.Fatal("download worker replaced live executable")
	}
	if !ReadJournal(dir).Seen[req.ID] {
		t.Fatal("request ID not durable")
	}
	if manager.Status().Phase != "installing" {
		t.Fatal("missing install status")
	}
}
