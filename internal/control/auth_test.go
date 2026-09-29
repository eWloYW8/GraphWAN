package control

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/store"
)

func TestSessionExpirationAndOrigin(t *testing.T) {
	a := &auth{sessions: map[[32]byte]session{tokenHash("expired"): {CSRF: "csrf", Expires: time.Now().Add(-time.Second)}}}
	req := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/state", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "expired"})
	if _, ok := a.find(req); ok {
		t.Fatal("expired session accepted")
	}
	for _, origin := range []string{"https://other.example", "http://example.com", "null", "https://user@example.com", "https://example.com/path"} {
		req.Header.Set("Origin", origin)
		if sameOrigin(req) {
			t.Fatalf("accepted origin %q", origin)
		}
	}
	req.Header.Set("Origin", "https://example.com")
	if !sameOrigin(req) {
		t.Fatal("rejected same origin")
	}
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if sameOrigin(req) {
		t.Fatal("accepted cross-site fetch")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := &auth{attempts: map[string]attempt{}}
	req := httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/login", nil)
	req.RemoteAddr = "192.0.2.1:54321"
	for range 10 {
		if !a.allowAttempt(req) {
			t.Fatal("limited before burst allowance")
		}
	}
	req.RemoteAddr = "192.0.2.1:54322"
	if a.allowAttempt(req) {
		t.Fatal("source port change bypassed limiter")
	}
	req.Header.Set("X-Forwarded-For", "192.0.2.2")
	if a.allowAttempt(req) {
		t.Fatal("untrusted forwarded address bypassed limiter")
	}
}

func TestStoredPasswordSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := newAuth(db, "long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	second, err := newAuth(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.password.Hash) != string(second.password.Hash) || string(first.password.Salt) != string(second.password.Salt) {
		t.Fatal("restart reset admin credentials")
	}
}

func TestOpenBrowserStreamExpiresWithSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, Options{Password: "browser-expiry-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	t.Cleanup(func() { app.Close(); server.Close(); db.Close() })
	app.auth.mu.Lock()
	app.auth.sessions[tokenHash("short-session")] = session{CSRF: "csrf", Expires: time.Now().Add(300 * time.Millisecond)}
	app.auth.mu.Unlock()
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "short-session"})
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Contains(data, []byte("event: snapshot")) {
		t.Fatalf("session expiry did not close an initialized stream: %v", err)
	}
}
