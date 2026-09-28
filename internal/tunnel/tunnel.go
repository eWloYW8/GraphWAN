// Package tunnel owns operating-system TUN interfaces and their virtual routes.
package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"regexp"

	"github.com/graphwan/graphwan/internal/model"
)

type Config struct {
	Name    string
	Address netip.Prefix
	MTU     int
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)

func (c Config) Validate() error {
	if c.Name != "" && !validName.MatchString(c.Name) {
		return errors.New("invalid TUN interface name")
	}
	if !c.Address.IsValid() || c.Address.Addr().IsUnspecified() || c.Address.Addr().IsMulticast() || c.Address.Addr().Is4In6() || c.Address.Bits() == 0 {
		return errors.New("invalid TUN virtual address")
	}
	if c.MTU < model.DefaultMTU || c.MTU > model.MaxMTU {
		return errors.New("invalid TUN MTU")
	}
	return nil
}
func (c Config) withName() Config {
	if c.Name == "" {
		var raw [6]byte
		rand.Read(raw[:])
		c.Name = "gw" + hex.EncodeToString(raw[:])
	}
	return c
}

type Device interface {
	io.ReadWriteCloser
	Name() string
	Configuration() Config
}
type Factory func(Config) (Device, error)
