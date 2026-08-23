//go:build windows

package mint

import (
	"errors"
	"strings"
)

var errWindowsSpentStore = errors.New("mint: the durable spent-token store is not available on Windows; run gateway and mint operator services on Linux")

// FileSpentSet is declared on Windows so client-only programs that import the
// mint protocol can build. The operator store itself fails closed: silently
// replacing Unix locking and file-identity checks would make tokens replayable.
type FileSpentSet struct{}

// OpenFileSpentSet refuses to open an operator redemption journal on Windows.
// Bearer never calls this function; mithlond will stop at startup with the
// explicit error until equivalent locking, ACL and replacement checks exist.
func OpenFileSpentSet(path string) (*FileSpentSet, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("mint: spent-token database path is required")
	}
	return nil, errWindowsSpentStore
}

func (*FileSpentSet) Spend(*Token) error  { return errWindowsSpentStore }
func (*FileSpentSet) Refund(*Token) error { return errWindowsSpentStore }
func (*FileSpentSet) Retire(string) (int, error) {
	return 0, errWindowsSpentStore
}
func (*FileSpentSet) Len() int     { return 0 }
func (*FileSpentSet) Close() error { return nil }

var _ RedemptionStore = (*FileSpentSet)(nil)
