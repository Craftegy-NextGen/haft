//go:build !windows

package cli

import (
	"fmt"
	"os"
)

func syncPublicExactFileDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open public exact file directory %s: %w", path, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync public exact file directory %s: %w", path, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close public exact file directory %s: %w", path, closeErr)
	}
	return nil
}

func syncProfileReviewDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
