//go:build !windows

package embedding

import "os"

func modeIsExecutable(info os.FileInfo) bool {
	return info.Mode().Perm()&0o111 != 0
}

// sidecarExecutablePath names the sidecar file at path. Outside Windows it is
// path itself.
func sidecarExecutablePath(path string) string {
	return path
}
