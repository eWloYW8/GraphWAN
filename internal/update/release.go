// Package update retrieves and verifies official GraphWAN release binaries.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

const LatestURL = "https://api.github.com/repos/eWloYW8/GraphWAN/releases/latest"

func GitHubClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 || r.URL.Scheme != "https" {
			return errors.New("invalid GitHub redirect")
		}
		switch r.URL.Host {
		case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		}
		return errors.New("unexpected GitHub download host")
	}}
}

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"assets"`
}
type Releases struct {
	mu        sync.Mutex
	metadata  release
	checked   time.Time
	Client    *http.Client
	Directory string
	cacheMu   sync.Mutex
}

func New(directory string) *Releases { return &Releases{Directory: directory, Client: GitHubClient()} }
func (s *Releases) Latest(ctx context.Context, goos, arch string) (model.ReleaseAsset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) > 5*time.Minute {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", LatestURL, nil)
		if err != nil {
			return model.ReleaseAsset{}, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "GraphWAN-updater")
		resp, err := s.Client.Do(req)
		if err != nil {
			return model.ReleaseAsset{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return model.ReleaseAsset{}, fmt.Errorf("GitHub latest release: HTTP %d", resp.StatusCode)
		}
		var next release
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&next); err != nil {
			return model.ReleaseAsset{}, err
		}
		if next.Draft || next.Prerelease {
			return model.ReleaseAsset{}, errors.New("latest release is not stable")
		}
		s.metadata = next
		s.checked = time.Now()
	}
	suffix := ""
	if goos == "windows" {
		suffix = ".exe"
	}
	name := "graphwan-" + s.metadata.Tag + "-" + goos + "-" + arch + suffix
	for _, asset := range s.metadata.Assets {
		if asset.Name == name {
			a := model.ReleaseAsset{Version: s.metadata.Tag, OS: goos, Arch: arch, Name: name, URL: asset.URL, Size: asset.Size, SHA256: strings.TrimPrefix(asset.Digest, "sha256:")}
			return a, a.Validate()
		}
	}
	return model.ReleaseAsset{}, fmt.Errorf("latest release has no binary for %s/%s", goos, arch)
}
func Verify(path string, a model.ReleaseAsset) error {
	if err := a.Validate(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("release binary size or SHA-256 mismatch")
	}
	return nil
}

// Fetch writes to a fresh staging file. It never replaces a running executable.
func Fetch(ctx context.Context, client *http.Client, url, path string, a model.ReleaseAsset) error {
	if err := a.Validate(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "GraphWAN-updater")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("release download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	good := false
	defer func() {
		f.Close()
		if !good {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("release binary size or SHA-256 mismatch")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	good = true
	return nil
}

// Cached serializes downloads so concurrent agents reuse one verified file.
// Entries are keyed by digest, survive restarts, and expire after seven days.
func (s *Releases) Cached(ctx context.Context, a model.ReleaseAsset) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.Directory == "" {
		return "", errors.New("release cache is not configured")
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(s.Directory, a.SHA256)
	if Verify(path, a) == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path, nil
	}
	_ = os.Remove(path)
	entries, _ := os.ReadDir(s.Directory)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() && time.Since(info.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(filepath.Join(s.Directory, e.Name()))
		}
	}
	temp, err := os.CreateTemp(s.Directory, "download-")
	if err != nil {
		return "", err
	}
	stage := temp.Name()
	temp.Close()
	os.Remove(stage)
	defer os.Remove(stage)
	if err := Fetch(ctx, s.Client, a.URL, stage, a); err != nil {
		return "", err
	}
	if err := os.Rename(stage, path); err != nil {
		return "", err
	}
	return path, nil
}

// Development revisions are explicitly replaceable; a released version must
// never silently downgrade to an older GitHub "latest" release.
func CheckVersion(current, target string) error {
	parse := func(v string) []string {
		v = strings.TrimPrefix(v, "v")
		v = strings.SplitN(v, "+", 2)[0]
		return strings.Split(v, ".")
	}
	a, b := parse(current), parse(target)
	if len(a) != 3 || len(b) != 3 {
		return nil
	}
	for i := range 3 {
		x, ok := new(big.Int).SetString(a[i], 10)
		if !ok {
			return nil
		}
		y, ok := new(big.Int).SetString(b[i], 10)
		if !ok {
			return nil
		}
		if x.Cmp(y) < 0 {
			return nil
		}
		if x.Cmp(y) > 0 {
			return errors.New("latest release is older than the installed version")
		}
	}
	return errors.New("already running the latest release")
}
