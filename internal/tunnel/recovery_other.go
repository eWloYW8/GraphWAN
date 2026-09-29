//go:build !openbsd

package tunnel

// Recover needs no journal for adapters whose kernel releases process-owned
// interfaces on descriptor/session close. Unsupported adapters still fail Open.
func Recover() error { return nil }
