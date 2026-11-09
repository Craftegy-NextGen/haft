//go:build !windows

package host

import "os"

func existingConfigModeChanged(stat os.FileInfo) bool {
	return stat.Mode().Perm() != 0600
}

func existingSkillModeChanged(info os.FileInfo) bool {
	return info.Mode().Perm() != 0600
}
