//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package host

import "fmt"

func lockInitRoot(_ string) (func(), error) {
	return nil, fmt.Errorf("host_init_unsupported: no qualified project lock on this platform")
}
