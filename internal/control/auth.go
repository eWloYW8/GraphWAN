package control

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/store"
	"golang.org/x/crypto/scrypt"
)

const cookieName = "graphwan_session"

type passwordRecord struct {
	Salt []byte `json:"salt"`
	Hash []byte `json:"hash"`
}
type session struct {
	CSRF    string
	Expires time.Time
}
type attempt struct {
	Count int
	Until time.Time
}
type auth struct {
	password passwordRecord
	mu       sync.Mutex
	sessions map[[32]byte]session
	attempts map[string]attempt
	slots    chan struct{}
}

func randomToken() string {
	var raw [32]byte
	rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}
func tokenHash(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func newAuth(db *store.Store, password string) (*auth, error) {
	a := &auth{sessions: map[[32]byte]session{}, attempts: map[string]attempt{}, slots: make(chan struct{}, 4)}
	err := db.WriteRecords(func(r *store.Records) error {
		if raw := r.Get("admin-password"); raw != nil {
			if err := json.Unmarshal(raw, &a.password); err != nil {
				return err
			}
			if len(a.password.Salt) != 32 || len(a.password.Hash) != 32 {
				return errors.New("invalid stored password hash")
			}
			return nil
		}
		if len(password) < 12 || len(password) > 1024 {
			return errors.New("first startup requires an admin password of 12–1024 bytes")
		}
		a.password.Salt = make([]byte, 32)
		rand.Read(a.password.Salt)
		hash, err := scrypt.Key([]byte(password), a.password.Salt, 32768, 8, 1, 32)
		if err != nil {
			return err
		}
		a.password.Hash = hash
		raw, err := json.Marshal(a.password)
		if err != nil {
			return err
		}
		return r.Put("admin-password", raw)
	})
	return a, err
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return err == nil && u.Scheme == scheme && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func (a *auth) allowAttempt(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, v := range a.attempts {
		if now.After(v.Until) {
			delete(a.attempts, key)
		}
	}
	v, exists := a.attempts[host]
	if !exists {
		if len(a.attempts) >= 4096 {
			return false
		}
		v.Until = now.Add(time.Minute)
	}
	v.Count++
	a.attempts[host] = v
	return v.Count <= 10
}
func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "cross-origin request rejected")
		return
	}
	if !a.allowAttempt(r) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "too many login attempts")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Password) > 1024 {
		fail(w, 401, "invalid password")
		return
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		fail(w, 429, "too many login attempts")
		return
	}
	hash, err := scrypt.Key([]byte(in.Password), a.password.Salt, 32768, 8, 1, 32)
	if err != nil || subtle.ConstantTimeCompare(hash, a.password.Hash) != 1 {
		fail(w, 401, "invalid password")
		return
	}
	token := randomToken()
	s := session{CSRF: randomToken(), Expires: time.Now().Add(12 * time.Hour)}
	a.mu.Lock()
	for k, v := range a.sessions {
		if time.Now().After(v.Expires) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 256 {
		a.mu.Unlock()
		fail(w, 429, "session limit reached")
		return
	}
	// Logging in rotates an existing session in the same browser.
	if c, err := r.Cookie(cookieName); err == nil {
		delete(a.sessions, tokenHash(c.Value))
	}
	a.sessions[tokenHash(token)] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Expires: s.Expires, MaxAge: 43200})
	respond(w, 200, map[string]string{"csrf_token": s.CSRF})
}
func (a *auth) find(r *http.Request) (session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) > 128 {
		return session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := tokenHash(c.Value)
	s, ok := a.sessions[key]
	if ok && time.Now().After(s.Expires) {
		delete(a.sessions, key)
		return session{}, false
	}
	return s, ok
}
func (a *auth) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.find(r)
		if !ok {
			fail(w, 401, "authentication required")
			return
		}
		if !sameOrigin(r) {
			fail(w, 403, "cross-origin request rejected")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) != 1 {
			fail(w, 403, "invalid CSRF token")
			return
		}
		next(w, r)
	}
}
func (a *auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, tokenHash(c.Value))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
