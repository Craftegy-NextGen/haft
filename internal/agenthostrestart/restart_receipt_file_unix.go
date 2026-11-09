//go:build darwin || linux

package agenthostrestart

import (
	"bytes"
	"os"
	"path/filepath"
)

func createPrivateRestartFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(privateFileMode); err != nil {
		_ = file.Close()
		return err
	}
	if err := requirePrivateRegularFile(file, "restart receipt"); err != nil {
		_ = file.Close()
		return err
	}
	writeErr := writeAndSync(file, content)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDirectory(filepath.Dir(path))
}

func replacePrivateRestartFileCAS(path string, expected []byte, proposed []byte) error {
	current, err := readPrivateRestartJSON(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return ErrConcurrentUpdate
	}
	stage, err := os.CreateTemp(
		filepath.Dir(path),
		restartReceiptStagePrefix,
	)
	if err != nil {
		return err
	}
	stagePath := stage.Name()
	defer func() {
		_ = os.Remove(stagePath)
	}()
	if err := stage.Chmod(0o600); err != nil {
		_ = stage.Close()
		return err
	}
	writeErr := writeAndSync(stage, proposed)
	closeErr := stage.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(stagePath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
