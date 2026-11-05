//go:build windows

package host

import "os"

// The fixtures write the existing carriers with mode 0600 and Init must keep
// that mode when it replaces them (publishInitFile chmods the stage to the
// destination's mode). 0600 has the owner-write bit, which Go reports on
// Windows as 0666, so these are the Windows images of the Unix test
// predicates in init_mode_unix_test.go.

// existingConfigModeChanged is the Windows image of
// `stat.Mode().Perm() != 0600`.
func existingConfigModeChanged(stat os.FileInfo) bool {
	return stat.Mode().Perm() != 0666
}

// existingSkillModeChanged is the Windows image of
// `info.Mode().Perm() != 0600`.
func existingSkillModeChanged(info os.FileInfo) bool {
	return info.Mode().Perm() != 0666
}
