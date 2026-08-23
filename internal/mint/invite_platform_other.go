//go:build !linux && !darwin

package mint

import (
	"errors"
	"os"
)

func invitePlatformCheck() error {
	return errors.New("mint: invite mode is supported only on Linux and macOS until owner-only secret-file permissions can be verified on this platform")
}

func inviteFileOwnedByTrustedUser(os.FileInfo) bool { return false }
