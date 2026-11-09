//go:build windows

// winport: mirrors internal/onboardingfs/filesystem_supported.go sha256:fae5805f26d3ed181a38a3e017126fa1ab94e72989ef2b40d301949002481659 (blob e05ea766fd469c8ebe4b4c7860687c1671f66fbe)

package onboardingfs

// This file is the Windows twin of filesystem_supported.go (ADR-091 R-c). The
// logic, order of effects, results and error texts are the same; the Unix
// calls map to internal/winfs (ADR-092):
//
//   - open(O_DIRECTORY|O_NOFOLLOW) and openat(O_DIRECTORY|O_NOFOLLOW):
//     winfs.OpenDir and Dir.OpenDir; a symbolic link or junction is ELOOP (G1)
//     and later effects are relative to the held handles (G2).
//   - openat(O_NOFOLLOW) of the carrier, a stage or the lock: Dir.OpenFile;
//     O_CREAT|O_EXCL is an exclusive create (G3); mode 0600 maps to
//     winfs.Private (explicit owner and protected owner-only DACL, G6 2a).
//   - fstat and fstatat(AT_SYMLINK_NOFOLLOW): winfs.Fstat and Dir.Lstat; the
//     (st_dev, st_ino) pair is winfs.FileID (O5).
//   - linkat (install without replace) and unlinkat: Dir.LinkIfAbsent and
//     Dir.Remove. On a volume without hard links LinkIfAbsent renames the stage
//     without replacement (winfs.Moved), which leaves the same end state; the
//     stage name is then already gone and is not unlinked (G4).
//   - flock(LOCK_EX), flock(LOCK_UN): winfs.LockFile with ExclusiveLock and
//     winfs.UnlockFile on the content-free lock file (G5).
//   - fsync of the .haft directory: Dir.Sync (G7, ADR-092 O3).
//
// heldDirectory keeps *winfs.Dir values where the Unix file keeps directory
// descriptors. checkedFileDescriptor keeps its Unix body, so a missing or
// closed handle is rejected with the same texts before any effect.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/onboarding"
	"github.com/m0n0x41d/haft/internal/winfs"
)

const (
	lockFileName = ".onboarding-memory-deferral-lock"
	stagePrefix  = ".onboarding-memory-deferral-stage-"
)

type heldDirectory struct {
	root     *winfs.Dir
	haft     *winfs.Dir
	lock     *os.File
	haftStat winfs.Info
}

func Read(projectRoot string) (ReadResult, error) {
	directory, err := openHeldDirectory(projectRoot, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = directory.close()
	}()
	return directory.readAttached()
}

func Install(
	projectRoot string,
	proposed onboarding.MemoryDeferral,
) (InstallResult, error) {
	proposedBytes, err := proposed.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	directory, err := openHeldDirectory(projectRoot, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = directory.close()
	}()
	if err := directory.lockExclusive(); err != nil {
		return nil, err
	}
	defer directory.unlock()
	current, err := directory.readAttached()
	if err != nil {
		return nil, err
	}
	switch value := current.(type) {
	case Present:
		currentBytes, encodeErr := value.Deferral.CanonicalJSON()
		if encodeErr != nil {
			return nil, encodeErr
		}
		if bytes.Equal(currentBytes, proposedBytes) {
			return Reused(value), nil
		}
		return Conflict{
			Current:  value.Deferral,
			Proposed: proposed,
		}, nil
	case Absent:
		return directory.installAbsent(proposed, proposedBytes)
	default:
		return nil, fmt.Errorf(
			"memory deferral read returned an unsupported state",
		)
	}
}

func Reopen(
	projectRoot string,
	expected onboarding.MemoryDeferral,
) (ReopenResult, error) {
	expectedBytes, err := expected.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	directory, err := openHeldDirectory(projectRoot, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = directory.close()
	}()
	if err := directory.lockExclusive(); err != nil {
		return nil, err
	}
	defer directory.unlock()
	current, err := directory.readAttached()
	if err != nil {
		return nil, err
	}
	switch value := current.(type) {
	case Absent:
		return AlreadyOpen{}, nil
	case Present:
		currentBytes, encodeErr := value.Deferral.CanonicalJSON()
		if encodeErr != nil {
			return nil, encodeErr
		}
		if !bytes.Equal(currentBytes, expectedBytes) {
			return ReopenConflict{
				Current:  value.Deferral,
				Expected: expected,
			}, nil
		}
	default:
		return nil, fmt.Errorf(
			"memory deferral read returned an unsupported state",
		)
	}
	haft, err := checkedDirectory(
		directory.haft,
		"onboarding state directory",
	)
	if err != nil {
		return nil, err
	}
	if err := haft.Remove(FileName); err != nil {
		return nil, fmt.Errorf(
			"remove exact memory deferral carrier: %w",
			err,
		)
	}
	if _, err := haft.Sync(); err != nil {
		return ReopenOutcomeUnknown{
			Expected: expected,
			Failure:  err.Error(),
		}, nil
	}
	observed, err := directory.readAttached()
	if errors.Is(err, os.ErrNotExist) {
		return Reopened{Deferral: expected}, nil
	}
	if err != nil {
		return ReopenOutcomeUnknown{
			Expected: expected,
			Failure:  err.Error(),
		}, nil
	}
	if _, absent := observed.(Absent); absent {
		return Reopened{Deferral: expected}, nil
	}
	return ReopenOutcomeUnknown{
		Expected: expected,
		Failure:  "carrier remained after exact reopen",
	}, nil
}

func (directory *heldDirectory) installAbsent(
	proposed onboarding.MemoryDeferral,
	proposedBytes []byte,
) (InstallResult, error) {
	haft, err := checkedDirectory(
		directory.haft,
		"onboarding state directory",
	)
	if err != nil {
		return nil, err
	}
	stage, stageName, err := directory.writeStage(
		haft,
		proposedBytes,
	)
	if err != nil {
		return nil, err
	}
	stagePresent := true
	defer func() {
		if stagePresent {
			_ = haft.Remove(stageName)
		}
	}()
	if err := stage.Close(); err != nil {
		return nil, fmt.Errorf(
			"close memory deferral stage: %w",
			err,
		)
	}
	if err := directory.requireAttached(); err != nil {
		return nil, err
	}
	linked, err := haft.LinkIfAbsent(
		stageName,
		haft,
		FileName,
	)
	if errors.Is(err, os.ErrExist) {
		current, readErr := directory.readAttached()
		if readErr != nil {
			return nil, readErr
		}
		present, ok := current.(Present)
		if !ok {
			return nil, fmt.Errorf(
				"memory deferral target appeared without readable bytes",
			)
		}
		currentBytes, encodeErr := present.Deferral.CanonicalJSON()
		if encodeErr != nil {
			return nil, encodeErr
		}
		if bytes.Equal(currentBytes, proposedBytes) {
			return Reused(present), nil
		}
		return Conflict{
			Current:  present.Deferral,
			Proposed: proposed,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"atomically install memory deferral carrier: %w",
			err,
		)
	}
	if linked == winfs.Moved {
		// The volume has no hard links: the stage was renamed to the carrier
		// name without replacement, so there is no stage name left to unlink.
		stagePresent = false
	}
	if stagePresent {
		if err := haft.Remove(stageName); err != nil {
			stagePresent = false
			return OutcomeUnknown{
				Proposed: proposed,
				Failure:  err.Error(),
			}, nil
		}
	}
	stagePresent = false
	if _, err := haft.Sync(); err != nil {
		return OutcomeUnknown{
			Proposed: proposed,
			Failure:  err.Error(),
		}, nil
	}
	observed, err := directory.readAttached()
	if err != nil {
		return OutcomeUnknown{
			Proposed: proposed,
			Failure:  err.Error(),
		}, nil
	}
	present, ok := observed.(Present)
	if !ok {
		return OutcomeUnknown{
			Proposed: proposed,
			Failure:  "installed carrier was absent on exact reread",
		}, nil
	}
	observedBytes, err := present.Deferral.CanonicalJSON()
	if err != nil || !bytes.Equal(observedBytes, proposedBytes) {
		return OutcomeUnknown{
			Proposed: proposed,
			Failure:  "installed carrier differed on exact reread",
		}, nil
	}
	return Created(present), nil
}

func (directory *heldDirectory) writeStage(
	haft *winfs.Dir,
	content []byte,
) (*os.File, string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, "", fmt.Errorf(
			"create memory deferral stage name: %w",
			err,
		)
	}
	name := stagePrefix + hex.EncodeToString(random)
	stage, err := haft.OpenFile(
		name,
		os.O_WRONLY|
			os.O_CREATE|
			os.O_EXCL,
		winfs.Private,
	)
	if err != nil {
		return nil, "", fmt.Errorf(
			"create memory deferral stage: %w",
			err,
		)
	}
	written, err := stage.Write(content)
	if err != nil {
		_ = stage.Close()
		_ = haft.Remove(name)
		return nil, "", fmt.Errorf(
			"write memory deferral stage: %w",
			err,
		)
	}
	if written != len(content) {
		_ = stage.Close()
		_ = haft.Remove(name)
		return nil, "", io.ErrShortWrite
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		_ = haft.Remove(name)
		return nil, "", fmt.Errorf(
			"sync memory deferral stage: %w",
			err,
		)
	}
	return stage, name, nil
}

func openHeldDirectory(
	projectRoot string,
	withLock bool,
) (*heldDirectory, error) {
	absolute, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve onboarding project root: %w",
			err,
		)
	}
	root, err := winfs.OpenDir(absolute)
	if err != nil {
		return nil, fmt.Errorf(
			"open onboarding project root: %w",
			err,
		)
	}
	haft, err := root.OpenDir(DirectoryName)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf(
			"open onboarding state directory: %w",
			err,
		)
	}
	stat, err := haft.Stat()
	if err != nil {
		_ = haft.Close()
		_ = root.Close()
		return nil, fmt.Errorf(
			"inspect onboarding state directory: %w",
			err,
		)
	}
	directory := &heldDirectory{
		root:     root,
		haft:     haft,
		haftStat: stat,
	}
	if !withLock {
		return directory, nil
	}
	lock, err := haft.OpenFile(
		lockFileName,
		os.O_RDWR|
			os.O_CREATE,
		winfs.Private,
	)
	if err != nil {
		_ = directory.close()
		return nil, fmt.Errorf(
			"open memory deferral lock: %w",
			err,
		)
	}
	directory.lock = lock
	return directory, nil
}

func (directory *heldDirectory) read() (ReadResult, error) {
	haft, err := checkedDirectory(
		directory.haft,
		"onboarding state directory",
	)
	if err != nil {
		return nil, err
	}
	file, err := haft.OpenFile(
		FileName,
		os.O_RDONLY,
		winfs.Inherited,
	)
	if errors.Is(err, os.ErrNotExist) {
		return Absent{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"open memory deferral carrier: %w",
			err,
		)
	}
	defer func() {
		_ = file.Close()
	}()
	stat, err := winfs.Fstat(file)
	if err != nil {
		return nil, fmt.Errorf(
			"inspect memory deferral carrier: %w",
			err,
		)
	}
	if !stat.IsRegular() {
		return nil, fmt.Errorf(
			"memory deferral carrier is not a regular file",
		)
	}
	if stat.Size > MaximumBytes {
		return nil, fmt.Errorf(
			"memory deferral carrier exceeds %d bytes",
			MaximumBytes,
		)
	}
	content, err := io.ReadAll(
		io.LimitReader(file, MaximumBytes+1),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"read memory deferral carrier: %w",
			err,
		)
	}
	if len(content) > MaximumBytes {
		return nil, fmt.Errorf(
			"memory deferral carrier exceeds %d bytes",
			MaximumBytes,
		)
	}
	deferral, err := onboarding.DecodeMemoryDeferral(content)
	if err != nil {
		return nil, err
	}
	return Present{Deferral: deferral}, nil
}

func (directory *heldDirectory) readAttached() (ReadResult, error) {
	if err := directory.requireAttached(); err != nil {
		return nil, err
	}
	result, err := directory.read()
	if err != nil {
		return nil, err
	}
	if err := directory.requireAttached(); err != nil {
		return nil, err
	}
	return result, nil
}

func (directory *heldDirectory) requireAttached() error {
	root, err := checkedDirectory(
		directory.root,
		"onboarding project root",
	)
	if err != nil {
		return err
	}
	current, err := root.Lstat(DirectoryName)
	if err != nil {
		return fmt.Errorf(
			"reinspect onboarding state directory: %w",
			err,
		)
	}
	if !current.IsDir() ||
		current.ID != directory.haftStat.ID {
		return fmt.Errorf(
			"onboarding state directory identity changed",
		)
	}
	return nil
}

func (directory *heldDirectory) lockExclusive() error {
	if directory.lock == nil {
		return fmt.Errorf("memory deferral lock is unavailable")
	}
	if _, err := checkedFileDescriptor(
		directory.lock,
		"memory deferral lock",
	); err != nil {
		return err
	}
	if err := winfs.LockFile(
		directory.lock,
		winfs.ExclusiveLock,
	); err != nil {
		return fmt.Errorf("lock memory deferral carrier: %w", err)
	}
	return nil
}

func (directory *heldDirectory) unlock() {
	if directory.lock == nil {
		return
	}
	if _, err := checkedFileDescriptor(
		directory.lock,
		"memory deferral lock",
	); err != nil {
		return
	}
	_ = winfs.UnlockFile(directory.lock)
}

func (directory *heldDirectory) close() error {
	if directory == nil {
		return nil
	}
	var lockErr error
	if directory.lock != nil {
		lockErr = directory.lock.Close()
	}
	haftErr := directory.haft.Close()
	rootErr := directory.root.Close()
	return errors.Join(lockErr, haftErr, rootErr)
}

// checkedDirectory is checkedFileDescriptor for a held directory: it rejects
// a missing or closed handle with the same texts and returns the directory
// for the handle-relative effect that follows.
func checkedDirectory(directory *winfs.Dir, subject string) (*winfs.Dir, error) {
	if directory == nil {
		return nil, fmt.Errorf("%s descriptor is unavailable", subject)
	}
	if _, err := checkedFileDescriptor(directory.File(), subject); err != nil {
		return nil, err
	}
	return directory, nil
}

func checkedFileDescriptor(file *os.File, subject string) (int, error) {
	if file == nil {
		return 0, fmt.Errorf("%s descriptor is unavailable", subject)
	}
	descriptor := file.Fd()
	if descriptor == ^uintptr(0) {
		return 0, fmt.Errorf("%s descriptor is closed", subject)
	}
	if descriptor > uintptr(math.MaxInt) {
		return 0, fmt.Errorf(
			"%s descriptor exceeds the platform int range",
			subject,
		)
	}
	return int(descriptor), nil
}

// newFileFromUnixDescriptor keeps the descriptor-adoption boundary of the Unix
// file, which the shared contract test exercises. winfs returns every handle
// already wrapped in an *os.File, so the Windows effects adopt no raw value;
// an unusable value is rejected with the same texts.
//
//nolint:unused // kept as the twin of the Unix helper, which the Windows effects do not call.
func newFileFromUnixDescriptor(
	descriptor int,
	name string,
) (*os.File, error) {
	if descriptor < 0 {
		return nil, fmt.Errorf("unix descriptor for %s is negative", name)
	}
	file := os.NewFile(uintptr(descriptor), name)
	if file == nil {
		return nil, fmt.Errorf("adopt unix descriptor for %s", name)
	}
	return file, nil
}
