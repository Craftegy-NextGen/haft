//go:build windows

package agenthostrestart

// Windows counterpart of restart_receipt_file_unix.go (ADR-091 R-b). The
// receipt files are files haft creates and later trusts, so they are created
// private (ADR-092 O2, 2a) rather than created with inherited security and
// checked afterwards:
//
//   - open(O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW, 0600) and the Chmod after it:
//     winfs.OpenFile with winfs.Private, which sets the owner and the
//     protected owner-only DACL as part of the create (G3, G6 2a).
//   - os.CreateTemp, Chmod and os.Rename (replace) of the CAS stage: an
//     exclusive private create of a random name relative to the held
//     directory, and Dir.Rename (G2, G4).
//   - fsync of the directory: syncDirectory, which goes through winfs (G7).

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

func createPrivateRestartFile(path string, content []byte) error {
	file, err := winfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Private)
	if err != nil {
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
	directory, err := winfs.OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	stageName, err := restartStageName(restartReceiptStagePrefix)
	if err != nil {
		return err
	}
	stage, err := directory.OpenFile(stageName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Private)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.Remove(stageName)
	}()
	writeErr := writeAndSync(stage, proposed)
	closeErr := stage.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := directory.Rename(stageName, directory, filepath.Base(path)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
