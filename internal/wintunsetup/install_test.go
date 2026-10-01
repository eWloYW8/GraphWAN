package wintunsetup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifiedArchiveInstallation(t *testing.T) {
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for name, content := range map[string]string{
		"wintun/LICENSE.txt":          "official license",
		"wintun/bin/amd64/wintun.dll": "amd64 DLL",
		"wintun/bin/arm64/wintun.dll": "arm64 DLL",
		"wintun/bin/x86/wintun.dll":   "x86 DLL",
		"../escape":                   "must not be extracted",
	} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := archive.Bytes()
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	for arch, want := range map[string]string{"amd64": "amd64 DLL", "arm64": "arm64 DLL", "386": "x86 DLL"} {
		t.Run(arch, func(t *testing.T) {
			dir := t.TempDir()
			installed, err := installArchive(dir, arch, raw, hash)
			if err != nil || !installed {
				t.Fatalf("install: %v %v", installed, err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "wintun.dll"))
			if err != nil || string(got) != want {
				t.Fatalf("wrong architecture: %q %v", got, err)
			}
			license, err := os.ReadFile(filepath.Join(dir, "WINTUN-LICENSE.txt"))
			if err != nil || string(license) != "official license" {
				t.Fatalf("license: %q %v", license, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("unexpected extracted files: %v %v", entries, err)
			}
			// An existing DLL must never be overwritten or trigger network/file reads.
			if err := os.WriteFile(filepath.Join(dir, "wintun.dll"), []byte("existing"), 0644); err != nil {
				t.Fatal(err)
			}
			installed, err = Ensure(context.Background(), dir, arch, "nonexistent.zip")
			if err != nil || installed {
				t.Fatalf("existing DLL not preserved: %v %v", installed, err)
			}
			installed, err = installArchive(dir, arch, raw, hash)
			if err != nil || installed {
				t.Fatalf("racing installer replaced DLL: %v %v", installed, err)
			}
			got, _ = os.ReadFile(filepath.Join(dir, "wintun.dll"))
			if string(got) != "existing" {
				t.Fatal("existing DLL changed")
			}
		})
	}
	dir := t.TempDir()
	if _, err := installArchive(dir, "amd64", raw, archiveSHA256); err == nil {
		t.Fatal("accepted untrusted archive")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("checksum failure wrote files")
	}
}

func TestArchiveReadLimit(t *testing.T) {
	if _, err := boundedRead(bytes.NewReader(make([]byte, maxArchive+1))); err == nil {
		t.Fatal("accepted oversized download/member")
	}
}
