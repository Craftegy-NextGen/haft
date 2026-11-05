//go:build windows

package ui

import (
	"fmt"
)

type termState struct{}

func makeRaw(fd uintptr) (*termState, error) {
	return nil, fmt.Errorf("terminal UI not supported on Windows")
}

func restore(fd uintptr, state *termState) error {
	return nil
}

func termSize() (int, int) {
	// Return default size - actual terminal detection would require Windows API
	return 80, 24
}