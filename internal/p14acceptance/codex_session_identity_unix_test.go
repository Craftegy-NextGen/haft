//go:build !windows

package p14acceptance

import (
	"fmt"
	"os"
	"syscall"
)

// Unix half of the ADR-093 E3 split of codex_session_history_test.go: the
// device and inode identity of a session history file. The function is moved
// verbatim; codex_session_identity_windows_test.go holds the Windows twin.

func p14CodexSessionIdentityFromFileInfo(
	info os.FileInfo,
) (p14CodexSessionFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return p14CodexSessionFileIdentity{}, fmt.Errorf(
			"P14 Codex session history has no stable device/inode identity",
		)
	}
	return p14CodexSessionFileIdentity{
		Device: uint64(stat.Dev),
		Inode:  uint64(stat.Ino),
	}, nil
}
