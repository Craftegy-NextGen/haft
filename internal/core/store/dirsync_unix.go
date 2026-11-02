//go:build !windows

package store

import "os"

func syncDir(root *os.Root, p string) error {
	f, err := root.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
