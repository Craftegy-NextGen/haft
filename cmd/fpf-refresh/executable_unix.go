//go:build !windows

package main

// indexerExecutablePath names the file the candidate indexer is built to.
// Outside Windows it is path itself.
func indexerExecutablePath(path string) string {
	return path
}
