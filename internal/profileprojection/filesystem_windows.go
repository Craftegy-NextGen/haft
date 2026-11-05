//go:build windows

// winport: mirrors internal/profileprojection/filesystem_supported.go sha256:6469e7d746b84c6714a1359346986a1b247707e086e5e4e9302153b57e050d9a (blob 5cae7d1be520f1208697c3e4520c6992c2f4be5e)

package profileprojection

// This file is the Windows twin of filesystem_supported.go (ADR-091 R-c). The
// logic, order of effects and error texts are the same; the Unix calls map to
// internal/winfs (ADR-092):
//
//   - open(O_DIRECTORY|O_NOFOLLOW) of the root and openat(O_DIRECTORY|
//     O_NOFOLLOW) of .haft: winfs.OpenDir and Dir.OpenDir; a symbolic link or
//     junction is ELOOP (G1) and every child effect is relative to the held
//     .haft handle (G2).
//   - mkdirat(0755): Dir.Mkdir with winfs.Inherited.
//   - openat(O_CREAT|O_EXCL|O_NOFOLLOW, 0644) of a stage: Dir.OpenFile with
//     os.O_CREATE|os.O_EXCL and winfs.Inherited (G3).
//   - renameat (replace) and unlinkat: Dir.Rename and Dir.Remove.
//   - fstat, lstat(path) and fstatat(AT_SYMLINK_NOFOLLOW): winfs.Fstat,
//     winfs.Lstat and Dir.Lstat; (st_dev, st_ino) is winfs.FileID (O5).
//   - fsync of a directory: Dir.Sync (G7, ADR-092 O3).

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/m0n0x41d/haft/internal/winfs"
)

const (
	projectionDirectoryName = ".haft"
	projectionFileName      = "project-profile.yaml"
	projectionStagePrefix   = ".project-profile.yaml.project-profile-projection-stage-"
	projectionStageSuffix   = ".tmp"
)

// projectionDirectory keeps both directory identities open for the complete
// observation/write/reread operation. All child effects are relative to the
// held handles and never resolve .haft by pathname after this capability is
// minted.
type projectionDirectory struct {
	rootPath string
	root     *winfs.Dir
	haft     *winfs.Dir
}

func openProjectionDirectory(projectRoot string) (*projectionDirectory, error) {
	root, err := openDirectoryNoFollow(projectRoot)
	if err != nil {
		return nil, err
	}
	haft, err := openHaftDirectory(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	directory := &projectionDirectory{rootPath: projectRoot, root: root, haft: haft}
	if err := directory.requireAttachedIdentity(); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func (directory *projectionDirectory) Close() error {
	if directory == nil {
		return nil
	}
	var haftErr error
	if directory.haft != nil {
		haftErr = directory.haft.Close()
		directory.haft = nil
	}
	var rootErr error
	if directory.root != nil {
		rootErr = directory.root.Close()
		directory.root = nil
	}
	return errors.Join(haftErr, rootErr)
}

func (directory *projectionDirectory) observe(expected []byte) projectionObservation {
	if err := directory.requireAttachedIdentity(); err != nil {
		return unreadableProjectionObservation(err)
	}
	content, err := directory.readTarget()
	if errors.Is(err, os.ErrNotExist) {
		return projectionObservation{
			kind:   observationMissing,
			detail: "canonical profile exists but its YAML projection is missing",
		}
	}
	if err != nil {
		return projectionObservation{
			kind:   observationUnreadable,
			detail: fmt.Sprintf("profile projection cannot be read safely: %v", err),
		}
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return unreadableProjectionObservation(err)
	}
	digest, err := digestProjection(content)
	if err != nil {
		return projectionObservation{
			kind:   observationUnreadable,
			detail: fmt.Sprintf("profile projection cannot be digested: %v", err),
		}
	}
	if bytes.Equal(content, expected) {
		return projectionObservation{
			kind:   observationMatched,
			digest: digest,
			detail: "profile projection exactly matches the canonical ledger revision",
		}
	}
	return projectionObservation{
		kind:   observationDrifted,
		digest: digest,
		detail: "profile projection bytes drift from the canonical ledger revision",
	}
}

func (directory *projectionDirectory) reconcileStages() error {
	if err := directory.valid(); err != nil {
		return err
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return err
	}
	entries, err := directory.haft.File().ReadDir(-1)
	if err != nil {
		return fmt.Errorf("enumerate profile projection stages: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, projectionStagePrefix) {
			continue
		}
		if !strings.HasSuffix(name, projectionStageSuffix) {
			continue
		}
		stat, err := directory.haft.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect stale profile projection stage: %w", err)
		}
		if !stat.IsRegular() {
			return fmt.Errorf("profile projection stage %s is not a regular file", name)
		}
		if err := directory.haft.Remove(name); err != nil {
			return fmt.Errorf("remove stale profile projection stage: %w", err)
		}
	}
	if err := syncDirectoryFile(directory.haft); err != nil {
		return err
	}
	return directory.requireAttachedIdentity()
}

func (directory *projectionDirectory) writeAtomic(
	content []byte,
	temporaryID string,
) error {
	if err := directory.valid(); err != nil {
		return err
	}
	if !validTemporaryIdentifier(temporaryID) {
		return fmt.Errorf("profile projection temporary identifier is invalid")
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return err
	}
	stageName := ".project-profile.yaml." + temporaryID + projectionStageSuffix
	stage, err := openExclusiveFileAt(directory.haft, stageName)
	if err != nil {
		return err
	}
	stagePresent := true
	defer func() {
		if stagePresent {
			_ = directory.haft.Remove(stageName)
		}
	}()
	writeErr := writeAndSync(stage, content)
	closeErr := stage.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return err
	}
	err = directory.haft.Rename(
		stageName,
		directory.haft,
		projectionFileName,
	)
	if err != nil {
		return fmt.Errorf("atomically install profile projection: %w", err)
	}
	stagePresent = false
	if err := syncDirectoryFile(directory.haft); err != nil {
		return err
	}
	observed, err := directory.readTarget()
	if err != nil {
		return err
	}
	if !bytes.Equal(observed, content) {
		return fmt.Errorf("profile projection reread differs from written bytes")
	}
	return directory.requireAttachedIdentity()
}

func (directory *projectionDirectory) readTarget() ([]byte, error) {
	if err := directory.valid(); err != nil {
		return nil, err
	}
	file, err := openReadOnlyFileAt(directory.haft, projectionFileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return content, nil
}

func (directory *projectionDirectory) valid() error {
	if directory == nil || directory.rootPath == "" || directory.root == nil || directory.haft == nil {
		return fmt.Errorf("profile projection directory capability is invalid")
	}
	return nil
}

func (directory *projectionDirectory) requireAttachedIdentity() error {
	if err := directory.valid(); err != nil {
		return err
	}
	heldRoot, err := directory.root.Stat()
	if err != nil {
		return fmt.Errorf("inspect held project root: %w", err)
	}
	pathRoot, err := winfs.Lstat(directory.rootPath)
	if err != nil {
		return fmt.Errorf("inspect project-root path identity: %w", err)
	}
	if heldRoot.ID != pathRoot.ID {
		return fmt.Errorf("project-root path identity changed during profile projection")
	}
	heldHaft, err := directory.haft.Stat()
	if err != nil {
		return fmt.Errorf("inspect held .haft directory: %w", err)
	}
	attachedHaft, err := directory.root.Lstat(projectionDirectoryName)
	if err != nil {
		return fmt.Errorf("inspect attached .haft identity: %w", err)
	}
	if heldHaft.ID != attachedHaft.ID {
		return fmt.Errorf(".haft directory identity changed during profile projection")
	}
	return nil
}

func openHaftDirectory(root *winfs.Dir) (*winfs.Dir, error) {
	haft, err := openDirectoryAt(root, projectionDirectoryName)
	if err == nil {
		return haft, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	err = root.Mkdir(projectionDirectoryName, winfs.Inherited)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create profile projection directory: %w", err)
	}
	if err := syncDirectoryFile(root); err != nil {
		return nil, err
	}
	return openDirectoryAt(root, projectionDirectoryName)
}

func openDirectoryNoFollow(path string) (*winfs.Dir, error) {
	directory, err := winfs.OpenDir(path)
	if err != nil {
		return nil, fmt.Errorf("open profile projection root without following symlinks: %w", err)
	}
	return directory, nil
}

func openDirectoryAt(parent *winfs.Dir, name string) (*winfs.Dir, error) {
	return parent.OpenDir(name)
}

func openExclusiveFileAt(parent *winfs.Dir, name string) (*os.File, error) {
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Inherited)
	if err != nil {
		return nil, fmt.Errorf("create profile projection stage: %w", err)
	}
	return file, nil
}

func openReadOnlyFileAt(parent *winfs.Dir, name string) (*os.File, error) {
	file, err := parent.OpenFile(name, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		return nil, err
	}
	stat, err := winfs.Fstat(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !stat.IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("profile projection is not a regular file")
	}
	return file, nil
}

func syncDirectoryFile(directory *winfs.Dir) error {
	if _, err := directory.Sync(); err != nil {
		return fmt.Errorf("sync profile projection directory: %w", err)
	}
	return nil
}

func validTemporaryIdentifier(value string) bool {
	return value != "" &&
		!strings.ContainsAny(value, `/\\`) &&
		!strings.Contains(value, "..")
}

func writeAndSync(file *os.File, content []byte) error {
	written, err := file.Write(content)
	if err != nil {
		return err
	}
	if written != len(content) {
		return io.ErrShortWrite
	}
	return file.Sync()
}
