package cluster

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

type Invitation struct {
	Token     string                `json:"token"`
	Directory model.ServerDirectory `json:"directory"`
}
type joinToken struct {
	Expires     time.Time `json:"expires"`
	ID          model.ID  `json:"id,omitempty"`
	PublicKey   []byte    `json:"public_key,omitempty"`
	Certificate []byte    `json:"certificate,omitempty"`
}
type joinRequest struct {
	Server model.Server `json:"server"`
	CSR    []byte       `json:"csr"`
}
type joinResponse struct {
	Image       store.Image `json:"image"`
	Certificate []byte      `json:"certificate"`
}
type pendingJoin struct {
	Identity Identity    `json:"identity"`
	Image    store.Image `json:"image"`
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "server-join/" + hex.EncodeToString(sum[:])
}
func (r *Runtime) Invite() (string, error) {
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	raw, _ := json.Marshal(joinToken{Expires: time.Now().Add(time.Hour)})
	if err := r.DB.WriteRecords(func(records *store.Records) error { return records.Put(tokenKey(token), raw) }); err != nil {
		return "", err
	}
	state, err := r.DB.Read()
	if err != nil {
		return "", err
	}
	raw, err = json.Marshal(Invitation{Token: token, Directory: *state.ServerDirectory()})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func ParseInvitation(raw string) (Invitation, error) {
	var in Invitation
	if len(raw) > 1<<20 {
		return in, errors.New("invitation too large")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return in, errors.New("invalid invitation")
	}
	if err = json.Unmarshal(data, &in); err != nil {
		return in, err
	}
	if len(in.Token) != 43 {
		return in, errors.New("invalid invitation token")
	}
	return in, in.Directory.Validate()
}
func (r *Runtime) RegisterJoin(mux *http.ServeMux, ca *pki.Authority) {
	mux.HandleFunc("POST /api/v1/cluster/enroll", func(w http.ResponseWriter, req *http.Request) {
		if req.TLS == nil {
			http.Error(w, "TLS required", 426)
			return
		}
		token := req.Header.Get("Authorization")
		if len(token) != 50 || token[:7] != "Bearer " {
			http.Error(w, "invitation required", 401)
			return
		}
		token = token[7:]
		if err := r.DB.ReadRecords(func(records *store.Records) error {
			var record joinToken
			if json.Unmarshal(records.Get(tokenKey(token)), &record) != nil || time.Now().After(record.Expires) {
				return errors.New("invalid invitation")
			}
			return nil
		}); err != nil {
			http.Error(w, "invalid invitation", 401)
			return
		}
		var input joinRequest
		if json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&input) != nil {
			http.Error(w, "invalid join", 400)
			return
		}
		if len(input.CSR) > 4096 || input.Server.Revoked || input.Server.Validate() != nil {
			http.Error(w, "invalid server identity", 400)
			return
		}
		certificate, pub, err := ca.IssueAgent(input.Server.ID, input.CSR)
		if err != nil || !pub.Equal(ed25519.PublicKey(input.Server.PublicKey)) {
			http.Error(w, "invalid server CSR", 400)
			return
		}
		for range 16 {
			state, err := r.DB.Read()
			if err != nil {
				http.Error(w, "read failed", 500)
				return
			}
			_, err = r.DB.UpdateWithRecords(state.Revision, func(state *model.State, records *store.Records) error {
				var saved joinToken
				if json.Unmarshal(records.Get(tokenKey(token)), &saved) != nil || time.Now().After(saved.Expires) {
					return errors.New("invalid invitation")
				}
				if saved.ID != "" {
					if saved.ID == input.Server.ID && bytes.Equal(saved.PublicKey, pub) {
						certificate = saved.Certificate
						return errJoinReplay
					}
					return errors.New("invitation already used")
				}
				for _, peer := range state.Servers {
					if peer.ID == input.Server.ID || bytes.Equal(peer.PublicKey, pub) {
						return errors.New("server already belongs to cluster")
					}
				}
				for _, agent := range state.Agents {
					if agent.ID == input.Server.ID || bytes.Equal(agent.PublicKey, pub) {
						return errors.New("server identity conflicts with agent")
					}
				}
				state.Servers = append(state.Servers, input.Server)
				saved.ID = input.Server.ID
				saved.PublicKey = pub
				saved.Certificate = certificate
				raw, _ := json.Marshal(saved)
				return records.Put(tokenKey(token), raw)
			})
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			if err != nil && !errors.Is(err, errJoinReplay) {
				status := 403
				if errors.Is(err, store.ErrUnavailable) {
					status = 503
				}
				http.Error(w, err.Error(), status)
				return
			}
			image, err := r.DB.Export()
			if err != nil {
				http.Error(w, "export failed", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(joinResponse{Image: image, Certificate: certificate})
			return
		}
		http.Error(w, "cluster busy", 409)
	})
}

var errJoinReplay = errors.New("join already committed")

// PrepareJoin only operates on an empty standalone controller. The encrypted
// response is staged locally; the supervisor installs it atomically on restart.
func (r *Runtime) PrepareJoin(ctx context.Context, encoded string) error {
	r.joinGate.Lock()
	defer r.joinGate.Unlock()
	if r.joining {
		return errors.New("join already prepared")
	}
	state, err := r.DB.Read()
	if err != nil {
		return err
	}
	if len(state.Agents) > 0 || len(state.Networks) > 0 || len(state.Servers) != 1 || r.Identity.Joined {
		return errors.New("only an empty standalone server can join a cluster")
	}
	invitation, err := ParseInvitation(encoded)
	if err != nil {
		return err
	}
	if invitation.Directory.ClusterID == state.ClusterID {
		return errors.New("server already belongs to this cluster")
	}
	csr, err := r.Identity.CSR()
	if err != nil {
		return err
	}
	input := joinRequest{Server: state.Servers[0], CSR: csr}
	raw, _ := json.Marshal(input)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(invitation.Directory.CA)
	var result joinResponse
	success := false
	for _, peer := range invitation.Directory.Servers {
		if peer.Revoked {
			continue
		}
		for _, ep := range peer.Endpoints {
			attempt, cancel := context.WithTimeout(ctx, 8*time.Second)
			tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: peer.TLSName(), MinVersion: tls.VersionTLS13}, DialContext: controltransport.DialContext(ep.Transport, roots, peer.TLSName()), TLSHandshakeTimeout: 3 * time.Second}
			client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			req, _ := http.NewRequestWithContext(attempt, "POST", ep.Origin()+"/api/v1/cluster/enroll", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+invitation.Token)
			resp, e := client.Do(req)
			if e == nil {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxStateBytes+1))
				resp.Body.Close()
				if readErr == nil && len(body) <= maxStateBytes && resp.StatusCode == 200 {
					e = json.Unmarshal(body, &result)
					success = e == nil
				} else {
					e = errors.New("join rejected or response unavailable")
				}
			}
			cancel()
			tr.CloseIdleConnections()
			err = e
			if success {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if success {
			break
		}
	}
	if !success {
		if err == nil {
			err = errors.New("no reachable invitation endpoints")
		}
		return err
	}
	if result.Image.State.ClusterID != invitation.Directory.ClusterID || !bytes.Equal(result.Image.State.ClusterCA, invitation.Directory.CA) {
		return errors.New("join response changed cluster identity")
	}
	if err = result.Image.State.Validate(); err != nil {
		return err
	}
	identity := r.Identity
	identity.Certificate = result.Certificate
	identity.Joined = true
	identity.Epoch = model.NewID()
	cert, err := identity.TLSCertificate()
	if err != nil {
		return err
	}
	if _, err = cert.Leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return err
	}
	found := false
	for _, peer := range result.Image.State.Servers {
		if peer.ID == identity.ID && !peer.Revoked && bytes.Equal(peer.PublicKey, identity.Public()) {
			found = true
		}
	}
	if !found {
		return errors.New("join response omits this server")
	}
	pending, _ := json.Marshal(pendingJoin{Identity: identity, Image: result.Image})
	if err := r.DB.PutLocal("join-pending", pending); err != nil {
		return err
	}
	r.joining = true
	return nil
}
func InstallPending(db *store.Store) error {
	raw, err := db.Local("join-pending")
	if err != nil || raw == nil {
		return err
	}
	var pending pendingJoin
	if err = json.Unmarshal(raw, &pending); err != nil {
		return err
	}
	identity, err := json.Marshal(pending.Identity)
	if err != nil {
		return err
	}
	return db.ImportWithLocal(pending.Image, map[string][]byte{"identity": identity, "join-pending": nil})
}
