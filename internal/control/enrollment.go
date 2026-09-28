package control

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/store"
)

type enrollmentToken struct {
	Expires time.Time `json:"expires"`
}

func enrollmentKey(token string) string {
	h := tokenHash(token)
	return "enrollment/" + hex.EncodeToString(h[:])
}
func (s *Server) createEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TTLSeconds == 0 {
		in.TTLSeconds = 3600
	}
	if in.TTLSeconds < 60 || in.TTLSeconds > 86400 {
		fail(w, 422, "token lifetime must be 60–86400 seconds")
		return
	}
	token := randomToken()
	record := enrollmentToken{Expires: time.Now().Add(time.Duration(in.TTLSeconds) * time.Second)}
	raw, err := json.Marshal(record)
	if err != nil {
		s.internal(w, err)
		return
	}
	if err := s.db.WriteRecords(func(r *store.Records) error { return r.Put(enrollmentKey(token), raw) }); err != nil {
		s.internal(w, err)
		return
	}
	respond(w, 201, map[string]any{"token": token, "expires_at": record.Expires})
}
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if !s.auth.allowAttempt(r) {
		fail(w, 429, "too many enrollment attempts")
		return
	}
	token := bearer(r)
	if token == "" {
		fail(w, 401, "enrollment token required")
		return
	}
	var in struct {
		Name string `json:"name"`
		CSR  []byte `json:"csr"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.CSR) > 4096 {
		fail(w, 400, "CSR too large")
		return
	}
	id := model.NewID()
	certificate, pub, err := s.ca.IssueAgent(id, in.CSR)
	if err != nil {
		fail(w, 400, "invalid Ed25519 certificate request")
		return
	}
	var state model.State
	for range 8 {
		current, err := s.db.Read()
		if err != nil {
			s.internal(w, err)
			return
		}
		state, err = s.db.UpdateWithRecords(current.Revision, func(state *model.State, records *store.Records) error {
			var record enrollmentToken
			if err := json.Unmarshal(records.Get(enrollmentKey(token)), &record); err != nil || !time.Now().Before(record.Expires) {
				return errInvalidToken
			}
			state.Agents = append(state.Agents, model.Agent{ID: id, Name: in.Name, PublicKey: pub, ListenPort: model.DefaultPort, Endpoints: []model.Endpoint{}})
			return records.Delete(enrollmentKey(token))
		})
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if errors.Is(err, errInvalidToken) {
			fail(w, 401, "invalid or expired enrollment token")
			return
		}
		if err != nil {
			fail(w, 422, err.Error())
			return
		}
		s.notify()
		respond(w, 201, map[string]any{"agent_id": id, "certificate": certificate, "ca_certificate": s.ca.PEM, "revision": state.Revision})
		return
	}
	fail(w, 409, "configuration is busy; retry enrollment")
}

var errInvalidToken = errors.New("invalid enrollment token")
