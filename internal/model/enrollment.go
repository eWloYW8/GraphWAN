package model

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const MaxEnrollmentInvitation = 1 << 20

// AgentInvitation carries public trust material and a secret one-use token.
// Base64 is an encoding, not encryption. Never include CA private keys.
type AgentInvitation struct {
	Kind      string          `json:"kind"`
	Version   int             `json:"version"`
	Token     string          `json:"token"`
	ExpiresAt time.Time       `json:"expires_at"`
	Directory ServerDirectory `json:"directory"`
}

func (i AgentInvitation) Encode() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > MaxEnrollmentInvitation {
		return "", errors.New("agent invitation too large")
	}
	return encoded, nil
}
func ParseAgentInvitation(raw string) (AgentInvitation, error) {
	var i AgentInvitation
	raw = strings.TrimSpace(raw)
	if len(raw) > MaxEnrollmentInvitation {
		return i, errors.New("agent invitation too large")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return i, errors.New("invalid agent invitation encoding")
	}
	if json.Unmarshal(data, &i) != nil {
		return i, errors.New("invalid agent invitation")
	}
	return i, i.Validate()
}
func (i AgentInvitation) Validate() error {
	if i.Kind != "graphwan-agent" || i.Version != 1 || len(i.Token) != 43 || i.ExpiresAt.IsZero() {
		return errors.New("invalid or unsupported agent invitation")
	}
	if _, err := base64.RawURLEncoding.DecodeString(i.Token); err != nil {
		return errors.New("invalid invitation token")
	}
	return i.Directory.Validate()
}
