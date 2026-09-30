// Package cluster replicates controller state with Raft. Every member exposes
// the same API; only the elected coordinator orders committed transactions.
package cluster

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

type Identity struct {
	ID          model.ID           `json:"id"`
	Name        string             `json:"name"`
	Key         ed25519.PrivateKey `json:"key"`
	Certificate []byte             `json:"certificate"`
	Epoch       model.ID           `json:"epoch"`
	Joined      bool               `json:"joined"`
}

func LoadIdentity(db *store.Store) (Identity, error) {
	var identity Identity
	raw, err := db.Local("identity")
	if err != nil {
		return identity, err
	}
	if raw != nil {
		if err = json.Unmarshal(raw, &identity); err != nil {
			return identity, err
		}
		if err = identity.ID.Validate(); err != nil {
			return identity, err
		}
		if len(identity.Key) != ed25519.PrivateKeySize {
			return identity, errors.New("invalid server private identity")
		}
		if err = identity.Epoch.Validate(); err != nil {
			return identity, err
		}
		if !bytes.Equal(identity.Key, ed25519.NewKeyFromSeed(identity.Key[:ed25519.SeedSize])) {
			return identity, errors.New("corrupt server private identity")
		}
		return identity, nil
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return identity, err
	}
	name, _ := os.Hostname()
	if name == "" {
		name = "server"
	}
	identity = Identity{ID: model.NewID(), Name: name, Key: key, Epoch: model.NewID()}
	return identity, identity.Save(db)
}
func (i Identity) Save(db *store.Store) error {
	raw, err := json.Marshal(i)
	if err != nil {
		return err
	}
	return db.PutLocal("identity", raw)
}
func (i Identity) Public() []byte { return i.Key.Public().(ed25519.PublicKey) }
func (i Identity) CSR() ([]byte, error) {
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, i.Key)
}
func (i Identity) TLSCertificate() (tls.Certificate, error) {
	b, _ := pem.Decode(i.Certificate)
	if b == nil {
		return tls.Certificate{}, errors.New("invalid server client certificate")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return tls.Certificate{}, err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !pub.Equal(ed25519.PublicKey(i.Public())) || cert.Subject.CommonName != string(i.ID) {
		return tls.Certificate{}, errors.New("server certificate identity mismatch")
	}
	return tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: i.Key, Leaf: cert}, nil
}

func Initialize(db *store.Store, ca *pki.Authority, identity *Identity, endpoints []model.ServerEndpoint) error {
	state, err := db.Read()
	if err != nil {
		return err
	}
	if state.ClusterID == "" {
		_, err = db.Update(state.Revision, func(state *model.State) error {
			state.ClusterID = model.NewID()
			state.ClusterCA = ca.PEM
			state.Servers = []model.Server{{ID: identity.ID, Name: identity.Name, PublicKey: identity.Public(), Endpoints: endpoints, STUNServers: []string{"tcp://stun.nextcloud.com:443"}}}
			return nil
		})
		if err != nil {
			return err
		}
	}
	state, err = db.Read()
	if err != nil {
		return err
	}
	if !bytes.Equal(state.ClusterCA, ca.PEM) {
		return errors.New("cluster CA does not match local signing authority")
	}
	found := false
	for _, peer := range state.Servers {
		if peer.ID == identity.ID && !peer.Revoked && bytes.Equal(peer.PublicKey, identity.Public()) {
			found = true
		}
	}
	if !found {
		return errors.New("local server identity absent from cluster")
	}
	if len(identity.Certificate) == 0 {
		csr, err := identity.CSR()
		if err != nil {
			return err
		}
		identity.Certificate, _, err = ca.IssueAgent(identity.ID, csr)
		if err != nil {
			return err
		}
		return identity.Save(db)
	}
	_, err = identity.TLSCertificate()
	return err
}
