package model

import (
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ReleaseAsset identifies an immutable download; both download paths verify it.
type ReleaseAsset struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

var releaseVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:\+[0-9A-Za-z.-]+)?$`)
var releasePlatform = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

func (a ReleaseAsset) Validate() error {
	suffix := ""
	if a.OS == "windows" {
		suffix = ".exe"
	}
	name := "graphwan-" + a.Version + "-" + a.OS + "-" + a.Arch + suffix
	digest, err := hex.DecodeString(a.SHA256)
	u, urlErr := url.Parse(a.URL)
	validURL := urlErr == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" && u.Path == "/eWloYW8/GraphWAN/releases/download/"+a.Version+"/"+name
	if len(a.Version) > 80 || !releaseVersion.MatchString(a.Version) || !releasePlatform.MatchString(a.OS) || !releasePlatform.MatchString(a.Arch) || a.Name != name || !validURL || err != nil || len(digest) != 32 || a.SHA256 != strings.ToLower(a.SHA256) || a.Size <= 0 || a.Size > 512<<20 {
		return errors.New("invalid release asset or missing SHA-256 digest")
	}
	return nil
}

type UpdateRequest struct {
	ID        ID           `json:"id"`
	Source    string       `json:"source"`
	Asset     ReleaseAsset `json:"asset"`
	CreatedAt time.Time    `json:"created_at"`
}

func (u UpdateRequest) Validate() error {
	if err := u.ID.Validate(); err != nil {
		return err
	}
	if u.Source != "github" && u.Source != "server" {
		return errors.New("update source must be github or server")
	}
	if u.CreatedAt.IsZero() {
		return errors.New("missing update creation time")
	}
	return u.Asset.Validate()
}

type UpdateStatus struct {
	Managed   bool      `json:"managed"`
	Service   string    `json:"service,omitempty"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	RequestID ID        `json:"request_id,omitempty"`
	Version   string    `json:"version,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}
