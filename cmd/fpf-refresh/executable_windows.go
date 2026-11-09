//go:build windows

package main

// indexerExecutablePath names the file the candidate indexer is built to.
// Windows runs a file by its extension (PATHEXT), and go build -o writes
// exactly the name it is given, so the extensionless base name would build a
// file that fpfrefresh.executableModeMissing rejects.
func indexerExecutablePath(path string) string {
	return path + ".exe"
}
