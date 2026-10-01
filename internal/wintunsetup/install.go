// Package wintunsetup installs the unchanged official Wintun distribution.
package wintunsetup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const Version = "0.14.1"
const downloadURL = "https://www.wintun.net/builds/wintun-" + Version + ".zip"
const archiveSHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
const maxArchive = 8 << 20

// Ensure installs beside the executable only when no DLL exists. archivePath
// optionally supplies an offline ZIP; it receives exactly the same validation.
func Ensure(ctx context.Context, directory, arch, archivePath string) (bool, error) {
	present, err := dllPresent(directory)
	if err != nil || present {
		return false, err
	}
	if _, err := archiveArchitecture(arch); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var raw []byte
	if archivePath != "" {
		f, err := os.Open(archivePath)
		if err != nil {
			return false, err
		}
		defer f.Close()
		raw, err = boundedRead(f)
		if err != nil {
			return false, err
		}
	} else {
		slog.Info("downloading Wintun", "version", Version, "architecture", arch)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return false, err
		}
		client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("Wintun redirect must use HTTPS")
			}
			if len(via) >= 5 {
				return errors.New("too many Wintun redirects")
			}
			return nil
		}}
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("Wintun download: HTTP %d", resp.StatusCode)
		}
		raw, err = boundedRead(resp.Body)
		if err != nil {
			return false, err
		}
	}
	return installArchive(directory, arch, raw, archiveSHA256)
}

func archiveArchitecture(arch string) (string, error) {
	switch arch {
	case "amd64", "arm64", "arm":
		return arch, nil
	case "386":
		return "x86", nil
	default:
		return "", fmt.Errorf("unsupported Wintun architecture %q", arch)
	}
}

func boundedRead(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxArchive+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxArchive {
		return nil, errors.New("Wintun archive/member exceeds 8 MiB")
	}
	return b, nil
}

func dllPresent(directory string) (bool, error) {
	info, err := os.Lstat(filepath.Join(directory, "wintun.dll"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("existing wintun.dll is not a regular file")
	}
	return true, nil
}

func installArchive(directory, arch string, raw []byte, expectedHash string) (bool, error) {
	if len(raw) > maxArchive || fmt.Sprintf("%x", sha256.Sum256(raw)) != expectedHash {
		return false, errors.New("Wintun archive SHA-256 mismatch; no files installed")
	}
	memberArch, err := archiveArchitecture(arch)
	if err != nil {
		return false, err
	}
	bundle, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return false, err
	}
	// Read only the two exact members, never extract archive-controlled paths.
	members := []string{"wintun/LICENSE.txt", "wintun/bin/" + memberArch + "/wintun.dll"}
	contents := make([][]byte, len(members))
	for i, name := range members {
		f, err := bundle.Open(name)
		if err != nil {
			return false, err
		}
		contents[i], err = boundedRead(f)
		f.Close()
		if err != nil {
			return false, err
		}
		if len(contents[i]) == 0 {
			return false, fmt.Errorf("empty Wintun member %s", name)
		}
	}
	if present, err := dllPresent(directory); err != nil || present {
		return false, err
	}
	// Publish the license before the DLL. Concurrent installers cannot replace
	// an existing DLL, including one already loaded by another Agent.
	if err := publish(directory, "WINTUN-LICENSE.txt", contents[0], true); err != nil {
		return false, err
	}
	if err := publish(directory, "wintun.dll", contents[1], false); err != nil {
		if present, checkErr := dllPresent(directory); checkErr == nil && present && errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func publish(directory, name string, contents []byte, replace bool) error {
	f, err := os.CreateTemp(directory, ".graphwan-wintun-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(contents); err != nil {
		return err
	}
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return moveFile(f.Name(), filepath.Join(directory, name), replace)
}
