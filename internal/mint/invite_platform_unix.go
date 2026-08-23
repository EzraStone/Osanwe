//go:build linux || darwin

package mint

import (
	"os"
	"syscall"
)

func invitePlatformCheck() error { return nil }

func inviteFileOwnedByTrustedUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid()))
}
