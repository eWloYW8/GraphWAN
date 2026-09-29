//go:build openbsd || netbsd

package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const persistentTunRegistry = "/var/run/graphwan-tun"

// The record is durable before creation; its lock remains held until cleanup.
// An interface is recoverable only after its description carries this random
// token and its original kernel index. Neither names nor PIDs prove ownership.
type tunLease struct {
	directory *os.File
	file      *os.File
	token     string
	name      string
}

func privateTunRecord(directory int, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(directory, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err == nil {
		if stat.Nlink == 0 {
			err = os.ErrNotExist // A live owner closed and unlinked after our openat.
		} else if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != 0 || stat.Nlink != 1 {
			err = errors.New("TUN ownership file must be a private root-owned regular file")
		}
	}
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func tunRegistryDirectory() (*os.File, error) {
	if err := os.Mkdir(persistentTunRegistry, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	fd, err := unix.Open(persistentTunRegistry, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err == nil && (stat.Uid != 0 || stat.Mode&0777 != 0700) {
		err = errors.New("TUN ownership directory must be private and root-owned")
	}
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), persistentTunRegistry), nil
}

// Recover releases orphaned marked interfaces before cached configuration is
// applied, including when the new snapshot no longer contains any Networks.
func Recover() error {
	if _, err := os.Lstat(persistentTunRegistry); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := nativeRecoveryContext(); err != nil {
		return err
	}
	directory, err := tunRegistryDirectory()
	if err != nil {
		return err
	}
	defer directory.Close()
	guard, err := lockTunRegistry(directory)
	if err != nil {
		return err
	}
	defer guard.Close()
	return recoverTunLeases(directory)
}

func lockTunRegistry(directory *os.File) (*os.File, error) {
	guard, err := privateTunRecord(int(directory.Fd()), ".lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	until := time.Now().Add(2 * time.Second)
	for {
		err = unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(until) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		guard.Close()
		return nil, fmt.Errorf("lock TUN registry: %w", err)
	}
	return guard, nil
}

func newTunLease(name string) (_ *tunLease, resultError error) {
	directory, err := tunRegistryDirectory()
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			directory.Close()
		}
	}()
	guard, err := lockTunRegistry(directory)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	if err := recoverTunLeases(directory); err != nil {
		return nil, err
	}
	var random [16]byte
	rand.Read(random[:])
	lease := &tunLease{directory: directory, token: hex.EncodeToString(random[:]), name: name}
	lease.file, err = privateTunRecord(int(directory.Fd()), lease.token, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !retained {
			resultError = errors.Join(resultError, unix.Unlinkat(int(directory.Fd()), lease.token, 0), lease.file.Close())
		}
	}()
	if err := unix.Flock(int(lease.file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	if err := lease.file.Sync(); err != nil {
		return nil, err
	}
	if err := directory.Sync(); err != nil {
		return nil, err
	}
	retained = true
	return lease, nil
}

func recoverTunLeases(directory *os.File) error {
	entries, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, token := range entries {
		raw, err := hex.DecodeString(token)
		if err != nil || len(raw) != 16 || hex.EncodeToString(raw) != token {
			continue
		}
		file, err := privateTunRecord(int(directory.Fd()), token, unix.O_RDWR)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			continue
		}
		if err == nil {
			err = recoverTunRecord(directory, token)
		}
		file.Close()
		if err != nil {
			return fmt.Errorf("recover TUN record %s: %w", token, err)
		}
	}
	return nil
}

func recoverTunRecord(directory *os.File, token string) error {
	// The filename is the durable random token; records deliberately have no
	// mutable payload. A crash during publication cannot leave partial metadata.
	// The kernel description supplies the name and original index for recovery.
	if err := cleanTunLease("", token); err != nil {
		return err
	}
	err := unix.Unlinkat(int(directory.Fd()), token, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *tunLease) close() error {
	err := cleanTunLease(l.name, l.token)
	// Keep the record when cleanup failed so the next startup can retry it.
	if err == nil {
		err = unix.Unlinkat(int(l.directory.Fd()), l.token, 0)
	}
	return errors.Join(err, l.file.Close(), l.directory.Close())
}

func cleanTunLease(name, token string) error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range interfaces {
		if name != "" && iface.Name != name {
			continue
		}
		if _, err := persistentTunUnit(iface.Name); err != nil {
			continue
		}
		description, err := nativeInterfaceDescription(iface.Name, "")
		if errors.Is(err, unix.ENXIO) {
			continue
		}
		if err != nil {
			return err
		}
		if description == tunOwnershipMarker(token, iface.Index) {
			if err := nativeDestroyPersistentInterface(iface.Name, iface.Index); err != nil {
				return err
			}
		}
	}
	return removeTunNode(token)
}

func removeTunNode(token string) error {
	// Resolve the private directory once without following symlinks. Never walk
	// through a replacement path to unlink another application's device node.
	path := filepath.Join("/dev", "graphwan-"+token)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Uid != 0 || stat.Mode&0777 != 0700 {
		return errors.New("private TUN device directory changed ownership")
	}
	if err := unix.Unlinkat(fd, "tun", 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Remove(path)
}
