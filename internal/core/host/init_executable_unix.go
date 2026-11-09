//go:build !windows

package host

import "os"

func candidateBinaryNotExecutable(bi os.FileInfo) bool {
	return bi.Mode()&0111 == 0
}
