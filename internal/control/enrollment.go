package control

import (
	"crypto/sha256"
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
	requestID := r.Header.Get("Idempotency-Key")
	if len(requestID) > 128 {
		fail(w, 400, "idempotency key too long")
		return
	}
	requestHash := sha256.Sum256(append(append([]byte{}, in.CSR...), []byte(in.Name)...))
	var receipt enrollmentReceipt
	var replayed bool
	var state model.State
	for range 8 {
		current, err := s.db.Read()
		if err != nil {
			s.internal(w, err)
			return
		}
		state, err = s.db.UpdateWithRecords(current.Revision, func(state *model.State, records *store.Records) error {
			if requestID != "" {
				var previous enrollmentReceipt
				if raw := records.Get(enrollmentKey(token) + "/receipt"); raw != nil {
					if json.Unmarshal(raw, &previous) != nil || previous.RequestID != requestID || previous.RequestHash != requestHash || !time.Now().Before(previous.Expires) {
						return errInvalidToken
					}
					for _, agent := range state.Agents {
						if agent.ID == previous.AgentID && !agent.Revoked {
							receipt = previous
							replayed = true
							return errEnrollmentReplay
						}
					}
					return errInvalidToken
				}
			}
			var record enrollmentToken
			if err := json.Unmarshal(records.Get(enrollmentKey(token)), &record); err != nil || !time.Now().Before(record.Expires) {
				return errInvalidToken
			}
			state.Agents = append(state.Agents, model.Agent{ID: id, Name: in.Name, PublicKey: pub, ListenPort: model.DefaultPort, Endpoints: []model.Endpoint{}})
			if requestID != "" {
				receipt = enrollmentReceipt{RequestID: requestID, RequestHash: requestHash, Expires: record.Expires, AgentID: id, Certificate: certificate, Revision: state.Revision + 1}
				raw, err := json.Marshal(receipt)
				if err != nil {
					return err
				}
				if err := records.Put(enrollmentKey(token)+"/receipt", raw); err != nil {
					return err
				}
			}
			return records.Delete(enrollmentKey(token))
		})
		if errors.Is(err, errEnrollmentReplay) && replayed {
			respond(w, 201, map[string]any{"agent_id": receipt.AgentID, "certificate": receipt.Certificate, "ca_certificate": s.ca.PEM, "revision": receipt.Revision})
			return
		}
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

// A receipt makes an exact enrollment retry recoverable after response loss.
// It never permits a second identity and expires with the original token.
type enrollmentReceipt struct {
	RequestID   string    `json:"request_id"`
	RequestHash [32]byte  `json:"request_hash"`
	Expires     time.Time `json:"expires"`
	AgentID     model.ID  `json:"agent_id"`
	Certificate []byte    `json:"certificate"`
	Revision    uint64    `json:"revision"`
}

var errEnrollmentReplay = errors.New("enrollment already committed")
