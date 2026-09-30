// Package bolt supplies the legacy Open/Options API imported by raft-boltdb's
// MigrateToV2 helper. GraphWAN never calls that helper; its live Raft store already
// uses bbolt directly. Delegating to the same bbolt avoids compiling the archived
// Bolt implementation, which lacks MIPS, RISC-V and LoongArch architecture files.
// This is a narrow dependency adapter, not a complete replacement for Bolt's API.
package bolt

import (
	"os"

	"go.etcd.io/bbolt"
)

type Options = bbolt.Options

func Open(path string, mode os.FileMode, options *Options) (*bbolt.DB, error) {
	return bbolt.Open(path, mode, options)
}
