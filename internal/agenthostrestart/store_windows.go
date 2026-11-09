//go:build windows

// winport: mirrors internal/agenthostrestart/store_unix.go sha256:d2fa3f142685f1cddc6318118aea2c0dd46755c978a572df918bfb46bb59953c (blob 28d9ecf9fad1215f112056ef2cd8336595003cfc)

package agenthostrestart

// This file is the Windows twin of store_unix.go (ADR-091 R-c). The logic,
// order of effects and error texts are the same; the Unix calls map to
// internal/winfs (ADR-092). Like the Unix store, it holds paths rather than
// descriptors, so each effect resolves its path and opens the final component
// without following a link; the attempt markers are opened relative to the
// held attempts directory, as the Unix code does with openat.
//
//   - open(O_NOFOLLOW) of the lock, the lease, the supervisor log and the
//     restart files: winfs.OpenFile; a symbolic link or junction at the final
//     component is ELOOP (G1). open(O_DIRECTORY|O_NOFOLLOW) is winfs.OpenDir
//     and openat of a marker is Dir.OpenFile on the held attempts directory
//     (winfs.DirFromFile), so the marker is relative to that handle (G2).
//   - mode 0600 and 0700 at creation, and the fstat mode checks of
//     requirePrivateRegularFile and requirePrivateDirectory: creation with
//     winfs.Private (explicit owner and protected owner-only DACL, G6 2a) and
//     winfs.CheckPrivate. The Chmod calls after creation are dropped: the
//     protection is part of the create.
//   - mkdir(0700): Dir.Mkdir on the held parent with winfs.Private. A file
//     created with mode 0644 (the restart .gitignore) is winfs.Inherited.
//   - O_CREAT|O_EXCL: os.O_CREATE|os.O_EXCL, an exclusive create (G3).
//   - os.CreateTemp of a checkpoint stage: an exclusive create of a random
//     name under the same prefix, and os.Rename (replace) is Dir.Rename (G4).
//   - flock(LOCK_EX), flock(LOCK_SH), flock(LOCK_EX|LOCK_NB), flock(LOCK_UN):
//     winfs.LockFile with ExclusiveLock or SharedLock, winfs.TryLockFile and
//     winfs.UnlockFile on the content-free lock files (G5). EWOULDBLOCK is
//     winfs.ErrBusy.
//   - fstat checks of the file type: winfs.Fstat and Info.IsRegular; the
//     lstat of a directory is winfs.Lstat (links and junctions are name
//     surrogates).
//   - fsync of a directory: winfs.SyncDirectory and Dir.Sync (G7, ADR-092
//     O3).
//
// The pure helpers (admitPreparation, ensureAttemptMarker, reserveAttemptMarker,
// attemptMarker, ignoreProtectsRestartFiles, loadUnlocked and writeAndSync)
// keep their Unix bodies.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/m0n0x41d/haft/internal/winfs"
)

const (
	RestartDirectoryName  = "restart"
	CheckpointFileName    = "checkpoint.json"
	ignoreFileName        = ".gitignore"
	lockFileName          = ".checkpoint.lock"
	supervisorLockName    = ".supervisor.lock"
	stagePrefix           = ".checkpoint-stage-"
	attemptsDirectoryName = "attempts"
	attemptMarkerPrefix   = "sha256-"
	attemptMarkerSuffix   = ".attempt"
	privateDirectoryMode  = 0o700
	privateFileMode       = 0o600
	maximumMarkerBytes    = 80
)

var restartIgnoreBytes = []byte("*\n!.gitignore\n")

// Store persists one current checkpoint under the project-local,
// gitignored .haft/restart directory.
type Store struct {
	projectRoot           string
	directoryPath         string
	attemptsDirectoryPath string
	checkpointPath        string
}

func NewStore(projectRoot string) (Store, error) {
	absoluteRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return Store{}, fmt.Errorf("resolve restart project root: %w", err)
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	if err := requireDirectoryNoSymlink(absoluteRoot, "project root"); err != nil {
		return Store{}, err
	}
	haftPath := filepath.Join(absoluteRoot, ".haft")
	if err := requireDirectoryNoSymlink(haftPath, ".haft directory"); err != nil {
		return Store{}, err
	}
	directoryPath := filepath.Join(haftPath, RestartDirectoryName)
	return Store{
		projectRoot:           absoluteRoot,
		directoryPath:         directoryPath,
		attemptsDirectoryPath: filepath.Join(directoryPath, attemptsDirectoryName),
		checkpointPath:        filepath.Join(directoryPath, CheckpointFileName),
	}, nil
}

func (store Store) CheckpointPath() string { return store.checkpointPath }

func (store Store) SupervisorLogPath(restartID string) (string, error) {
	if !exactToken.MatchString(restartID) {
		return "", fmt.Errorf("restart_id is invalid")
	}
	return filepath.Join(store.directoryPath, restartID+".log"), nil
}

// Prepare writes a new attempt only when no active handoff exists and the
// desired digest has never been consumed by this store.
func (store Store) Prepare(checkpoint Checkpoint) error {
	if err := checkpoint.validate(); err != nil {
		return err
	}
	if checkpoint.state != StatePrepared || checkpoint.attempt != checkpointAttempt {
		return fmt.Errorf("%w: only a prepared attempt=1 checkpoint can be installed", ErrInvalidTransition)
	}
	if checkpoint.repositoryRoot != store.projectRoot {
		return fmt.Errorf("restart checkpoint belongs to another repository")
	}
	expectedLogPath, err := store.SupervisorLogPath(checkpoint.restartID)
	if err != nil {
		return err
	}
	if checkpoint.supervisorLogPath != expectedLogPath {
		return fmt.Errorf("restart checkpoint supervisor log path is not store-owned")
	}
	return store.withExclusiveLock(func() error {
		current, loadErr := store.loadUnlocked()
		if loadErr != nil && !errors.Is(loadErr, ErrCheckpointNotFound) {
			return loadErr
		}
		if loadErr == nil {
			if err := admitPreparation(current, checkpoint); err != nil {
				return err
			}
		}
		attempts, err := store.openAttemptsDirectory()
		if err != nil {
			return err
		}
		defer attempts.Close()
		if loadErr == nil {
			if err := ensureAttemptMarker(attempts, current.desiredHaftBinaryDigest); err != nil {
				return err
			}
		}
		if err := reserveAttemptMarker(attempts, checkpoint.desiredHaftBinaryDigest); err != nil {
			return err
		}
		return store.writeUnlocked(checkpoint)
	})
}

// Apply is an exact-byte compare-and-swap for one closed state transition.
// Replays are idempotent except for the resumed-turn single-writer claim.
func (store Store) Apply(change Change) error {
	if !change.valid() {
		return fmt.Errorf("%w: proposed durable transition is not legal", ErrInvalidTransition)
	}
	expected := change.before
	proposed := change.after
	if err := expected.validate(); err != nil {
		return err
	}
	if err := proposed.validate(); err != nil {
		return err
	}
	return store.withExclusiveLock(func() error {
		current, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		currentBytes, err := current.CanonicalBytes()
		if err != nil {
			return err
		}
		proposedBytes, err := proposed.CanonicalBytes()
		if err != nil {
			return err
		}
		if bytes.Equal(currentBytes, proposedBytes) {
			if expected.state == StateAppOpened && proposed.state == StateResumed {
				return ErrDuplicateResumeWriter
			}
			return nil
		}
		expectedBytes, err := expected.CanonicalBytes()
		if err != nil {
			return err
		}
		if !bytes.Equal(currentBytes, expectedBytes) {
			return ErrConcurrentUpdate
		}
		return store.writeUnlocked(proposed)
	})
}

func (store Store) Load() (Checkpoint, error) {
	if err := store.requireExistingDirectory(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Checkpoint{}, ErrCheckpointNotFound
		}
		return Checkpoint{}, err
	}
	lock, err := store.openExistingLock()
	if err != nil {
		return Checkpoint{}, err
	}
	defer lock.Close()
	if err := winfs.LockFile(lock, winfs.SharedLock); err != nil {
		return Checkpoint{}, fmt.Errorf("lock restart checkpoint for read: %w", err)
	}
	defer func() {
		_ = winfs.UnlockFile(lock)
	}()
	return store.loadUnlocked()
}

// OpenSupervisorLog opens only the store-owned log path from the exact
// checkpoint. The log remains inside the gitignored restart directory.
func (store Store) OpenSupervisorLog(checkpoint Checkpoint) (*os.File, error) {
	if err := store.requireExistingDirectory(); err != nil {
		return nil, err
	}
	if checkpoint.repositoryRoot != store.projectRoot {
		return nil, fmt.Errorf("restart checkpoint belongs to another repository")
	}
	expectedPath, err := store.SupervisorLogPath(checkpoint.restartID)
	if err != nil {
		return nil, err
	}
	if checkpoint.supervisorLogPath != expectedPath {
		return nil, fmt.Errorf("restart checkpoint supervisor log path is not store-owned")
	}
	file, err := winfs.OpenFile(expectedPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, winfs.Private)
	if err != nil {
		return nil, fmt.Errorf("open restart supervisor log: %w", err)
	}
	if err := requirePrivateRegularFile(file, "restart supervisor log"); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (store Store) withSupervisorLease(
	effect func() (Checkpoint, error),
) (Checkpoint, error) {
	if err := store.requireExistingDirectory(); err != nil {
		return Checkpoint{}, err
	}
	path := filepath.Join(store.directoryPath, supervisorLockName)
	lease, err := winfs.OpenFile(path, os.O_RDWR|os.O_CREATE, winfs.Private)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("open restart supervisor lease: %w", err)
	}
	defer lease.Close()
	if err := requirePrivateRegularFile(lease, "restart supervisor lease"); err != nil {
		return Checkpoint{}, err
	}
	if err := winfs.TryLockFile(lease, winfs.ExclusiveLock); err != nil {
		if errors.Is(err, winfs.ErrBusy) {
			return Checkpoint{}, ErrDuplicateSupervisor
		}
		return Checkpoint{}, fmt.Errorf("acquire restart supervisor lease: %w", err)
	}
	defer func() {
		_ = winfs.UnlockFile(lease)
	}()
	return effect()
}

func admitPreparation(current Checkpoint, proposed Checkpoint) error {
	switch current.state {
	case StatePrepared, StateSubmitted, StateAppOpened, StateResumed:
		return fmt.Errorf("%w: restart %s remains %s", ErrLoopGuard, current.restartID, current.state.String())
	case StateInstallFailed:
		if current.desiredHaftBinaryDigest == proposed.desiredHaftBinaryDigest {
			return fmt.Errorf("%w: desired digest already consumed attempt=1", ErrLoopGuard)
		}
		return nil
	case StateVerified:
		if current.desiredHaftBinaryDigest == proposed.desiredHaftBinaryDigest {
			return ErrAlreadyVerified
		}
		return nil
	default:
		return fmt.Errorf("%w: current checkpoint state is invalid", ErrLoopGuard)
	}
}

func (store Store) withExclusiveLock(effect func() error) error {
	if err := store.ensureDirectory(); err != nil {
		return err
	}
	lock, err := store.openLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := winfs.LockFile(lock, winfs.ExclusiveLock); err != nil {
		return fmt.Errorf("lock restart checkpoint for write: %w", err)
	}
	defer func() {
		_ = winfs.UnlockFile(lock)
	}()
	return effect()
}

func (store Store) ensureDirectory() error {
	if err := requireDirectoryNoSymlink(store.projectRoot, "project root"); err != nil {
		return err
	}
	haftPath := filepath.Join(store.projectRoot, ".haft")
	if err := requireDirectoryNoSymlink(haftPath, ".haft directory"); err != nil {
		return err
	}
	haft, err := winfs.OpenDir(haftPath)
	if err != nil {
		return fmt.Errorf("open .haft directory without following symlinks: %w", err)
	}
	mkdirErr := haft.Mkdir(RestartDirectoryName, winfs.Private)
	_ = haft.Close()
	if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
		return fmt.Errorf("create restart checkpoint directory: %w", mkdirErr)
	}
	if err := requirePrivateDirectoryNoSymlink(store.directoryPath, "restart checkpoint directory"); err != nil {
		return err
	}
	return store.ensureIgnoreFile()
}

func (store Store) requireExistingDirectory() error {
	if err := requireDirectoryNoSymlink(store.projectRoot, "project root"); err != nil {
		return err
	}
	haftPath := filepath.Join(store.projectRoot, ".haft")
	if err := requireDirectoryNoSymlink(haftPath, ".haft directory"); err != nil {
		return err
	}
	if err := requirePrivateDirectoryNoSymlink(store.directoryPath, "restart checkpoint directory"); err != nil {
		return err
	}
	content, err := readRegularNoFollow(
		filepath.Join(store.directoryPath, ignoreFileName),
		MaximumCheckpointBytes,
	)
	if err != nil {
		return err
	}
	if !ignoreProtectsRestartFiles(content) {
		return fmt.Errorf("restart .gitignore does not ignore checkpoint and log files")
	}
	return nil
}

func (store Store) ensureIgnoreFile() error {
	path := filepath.Join(store.directoryPath, ignoreFileName)
	content, err := readRegularNoFollow(path, MaximumCheckpointBytes)
	if err == nil {
		if !ignoreProtectsRestartFiles(content) {
			return fmt.Errorf("restart .gitignore does not ignore checkpoint and log files")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, openErr := winfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Inherited)
	if openErr != nil {
		if errors.Is(openErr, os.ErrExist) {
			return store.ensureIgnoreFile()
		}
		return fmt.Errorf("create restart .gitignore: %w", openErr)
	}
	writeErr := writeAndSync(file, restartIgnoreBytes)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDirectory(store.directoryPath)
}

func (store Store) openAttemptsDirectory() (*os.File, error) {
	parent, err := winfs.OpenDir(store.directoryPath)
	if err != nil {
		return nil, fmt.Errorf("open restart checkpoint directory without following symlinks: %w", err)
	}
	mkdirErr := parent.Mkdir(attemptsDirectoryName, winfs.Private)
	_ = parent.Close()
	if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
		return nil, fmt.Errorf("create restart attempts directory: %w", mkdirErr)
	}
	if err := syncDirectory(store.directoryPath); err != nil {
		return nil, fmt.Errorf("sync restart directory after attempts directory: %w", err)
	}
	held, err := winfs.OpenDir(store.attemptsDirectoryPath)
	if err != nil {
		return nil, fmt.Errorf("open restart attempts directory without following symlinks: %w", err)
	}
	directory := held.File()
	if err := requirePrivateDirectory(directory, "restart attempts directory"); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func ensureAttemptMarker(directory *os.File, desiredDigest string) error {
	err := createAttemptMarker(directory, desiredDigest)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	return validateAttemptMarker(directory, desiredDigest)
}

func reserveAttemptMarker(directory *os.File, desiredDigest string) error {
	err := createAttemptMarker(directory, desiredDigest)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := validateAttemptMarker(directory, desiredDigest); err != nil {
		return err
	}
	return fmt.Errorf("%w: desired digest already consumed attempt=1", ErrLoopGuard)
}

func createAttemptMarker(directory *os.File, desiredDigest string) error {
	name, content, err := attemptMarker(desiredDigest)
	if err != nil {
		return err
	}
	held, err := winfs.DirFromFile(directory)
	if err != nil {
		return fmt.Errorf("reserve restart attempt marker: %w", err)
	}
	marker, err := held.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Private)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return fmt.Errorf("reserve restart attempt marker: %w", err)
	}
	if err := requirePrivateRegularFile(marker, "restart attempt marker"); err != nil {
		_ = marker.Close()
		return err
	}
	writeErr := writeAndSync(marker, content)
	closeErr := marker.Close()
	if writeErr != nil {
		return fmt.Errorf("write restart attempt marker: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close restart attempt marker: %w", closeErr)
	}
	if _, err := held.Sync(); err != nil {
		return fmt.Errorf("sync restart attempts directory: %w", err)
	}
	return nil
}

func validateAttemptMarker(directory *os.File, desiredDigest string) error {
	name, expected, err := attemptMarker(desiredDigest)
	if err != nil {
		return err
	}
	held, err := winfs.DirFromFile(directory)
	if err != nil {
		return fmt.Errorf("open existing restart attempt marker without following symlinks: %w", err)
	}
	marker, err := held.OpenFile(name, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		return fmt.Errorf("open existing restart attempt marker without following symlinks: %w", err)
	}
	defer marker.Close()
	if err := requirePrivateRegularFile(marker, "restart attempt marker"); err != nil {
		return err
	}
	reader := io.LimitReader(marker, maximumMarkerBytes+1)
	content, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read restart attempt marker: %w", err)
	}
	if len(content) > maximumMarkerBytes {
		return fmt.Errorf("restart attempt marker exceeds %d bytes", maximumMarkerBytes)
	}
	if !bytes.Equal(content, expected) {
		return fmt.Errorf("restart attempt marker does not match desired digest")
	}
	return nil
}

func attemptMarker(desiredDigest string) (string, []byte, error) {
	if !exactSHA256Digest.MatchString(desiredDigest) {
		return "", nil, fmt.Errorf("restart attempt marker digest is invalid")
	}
	hexDigest := strings.TrimPrefix(desiredDigest, "sha256:")
	name := attemptMarkerPrefix + hexDigest + attemptMarkerSuffix
	content := []byte(desiredDigest + "\n")
	return name, content, nil
}

func ignoreProtectsRestartFiles(content []byte) bool {
	lines := strings.Split(string(content), "\n")
	matcher := ignore.CompileIgnoreLines(lines...)
	return matcher.MatchesPath(CheckpointFileName) && matcher.MatchesPath("restart.log")
}

func (store Store) openLock() (*os.File, error) {
	path := filepath.Join(store.directoryPath, lockFileName)
	file, err := winfs.OpenFile(path, os.O_RDWR|os.O_CREATE, winfs.Private)
	if err != nil {
		return nil, fmt.Errorf("open restart checkpoint lock: %w", err)
	}
	if err := requirePrivateRegularFile(file, "restart checkpoint lock"); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (store Store) openExistingLock() (*os.File, error) {
	path := filepath.Join(store.directoryPath, lockFileName)
	file, err := winfs.OpenFile(path, os.O_RDWR, winfs.Inherited)
	if err != nil {
		return nil, fmt.Errorf("open existing restart checkpoint lock: %w", err)
	}
	if err := requirePrivateRegularFile(file, "restart checkpoint lock"); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (store Store) loadUnlocked() (Checkpoint, error) {
	content, err := readPrivateRegularNoFollow(
		store.checkpointPath,
		MaximumCheckpointBytes,
		"restart checkpoint",
	)
	if errors.Is(err, os.ErrNotExist) {
		return Checkpoint{}, ErrCheckpointNotFound
	}
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint, err := DecodeCheckpoint(content)
	if err != nil {
		return Checkpoint{}, err
	}
	if checkpoint.repositoryRoot != store.projectRoot {
		return Checkpoint{}, fmt.Errorf("stored restart checkpoint belongs to another repository")
	}
	return checkpoint, nil
}

func (store Store) writeUnlocked(checkpoint Checkpoint) error {
	content, err := checkpoint.CanonicalBytes()
	if err != nil {
		return err
	}
	directory, err := winfs.OpenDir(store.directoryPath)
	if err != nil {
		return fmt.Errorf("open restart checkpoint directory without following symlinks: %w", err)
	}
	defer directory.Close()
	stageName, err := restartStageName(stagePrefix)
	if err != nil {
		return fmt.Errorf("create restart checkpoint stage: %w", err)
	}
	stage, err := directory.OpenFile(stageName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Private)
	if err != nil {
		return fmt.Errorf("create restart checkpoint stage: %w", err)
	}
	stagePresent := true
	defer func() {
		if stagePresent {
			_ = directory.Remove(stageName)
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
	if err := directory.Rename(stageName, directory, CheckpointFileName); err != nil {
		return fmt.Errorf("atomically install restart checkpoint: %w", err)
	}
	stagePresent = false
	if err := syncDirectory(store.directoryPath); err != nil {
		return err
	}
	reread, err := readPrivateRegularNoFollow(
		store.checkpointPath,
		MaximumCheckpointBytes,
		"restart checkpoint",
	)
	if err != nil {
		return err
	}
	if !bytes.Equal(reread, content) {
		return fmt.Errorf("restart checkpoint reread differs from written bytes")
	}
	return nil
}

func requireDirectoryNoSymlink(path string, label string) error {
	info, err := winfs.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.IsNameSurrogate() || !info.IsDir() {
		return fmt.Errorf("%s is not a non-symlink directory", label)
	}
	return nil
}

func requirePrivateDirectoryNoSymlink(path string, label string) error {
	if err := requireDirectoryNoSymlink(path, label); err != nil {
		return err
	}
	directory, err := winfs.OpenDir(path)
	if err != nil {
		return fmt.Errorf("open %s without following symlinks: %w", label, err)
	}
	defer directory.Close()
	return requirePrivateDirectory(directory.File(), label)
}

func requirePrivateDirectory(directory *os.File, label string) error {
	info, err := winfs.Fstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", label)
	}
	if err := winfs.CheckPrivate(directory); err != nil {
		return fmt.Errorf("%s ownership or mode is unsafe: %w", label, err)
	}
	return nil
}

func readRegularNoFollow(path string, maximum int) ([]byte, error) {
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("open restart file without following symlinks: %w", err)
	}
	defer file.Close()
	if err := requireRegularFile(file, "restart file"); err != nil {
		return nil, err
	}
	reader := io.LimitReader(file, int64(maximum+1))
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(content) > maximum {
		return nil, fmt.Errorf("restart file exceeds %d bytes", maximum)
	}
	return content, nil
}

func readPrivateRegularNoFollow(
	path string,
	maximum int,
	label string,
) ([]byte, error) {
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("open %s without following symlinks: %w", label, err)
	}
	defer file.Close()
	if err := requirePrivateRegularFile(file, label); err != nil {
		return nil, err
	}
	reader := io.LimitReader(file, int64(maximum+1))
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(content) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return content, nil
}

func requireRegularFile(file *os.File, label string) error {
	info, err := winfs.Fstat(file)
	if err != nil {
		return err
	}
	if !info.IsRegular() {
		return fmt.Errorf("%s is not a regular file", label)
	}
	return nil
}

func requirePrivateRegularFile(file *os.File, label string) error {
	info, err := winfs.Fstat(file)
	if err != nil {
		return err
	}
	if !info.IsRegular() {
		return fmt.Errorf("%s is not a regular file", label)
	}
	if err := winfs.CheckPrivate(file); err != nil {
		return fmt.Errorf("%s ownership or mode is unsafe: %w", label, err)
	}
	return nil
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

// syncDirectory is fsync on a directory (ADR-092 O3). Windows cannot flush the
// read-only handle that os.Open returns, so the flush goes through
// winfs.SyncDirectory. Every error reaches the caller, as on Unix.
func syncDirectory(path string) error {
	_, err := winfs.SyncDirectory(path)
	return err
}

// restartStageName is the Windows counterpart of the random suffix that
// os.CreateTemp appends: stages are created exclusively under this name.
func restartStageName(prefix string) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(suffix), nil
}
