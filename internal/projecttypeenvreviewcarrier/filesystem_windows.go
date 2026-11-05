//go:build windows

// winport: mirrors internal/projecttypeenvreviewcarrier/filesystem_supported.go sha256:883cbd31fa857e35670723b87bbd700d2c323a87d614183e960e7a4e95afce2a (blob 09ba5d4bc43fdf019939d370054b11a34a4871fc)

package projecttypeenvreviewcarrier

// This file is the Windows twin of filesystem_supported.go (ADR-091 R-c). The
// logic, order of effects, typed results and error texts are the same; the
// Unix calls map to internal/winfs (ADR-092):
//
//   - open(O_DIRECTORY|O_NOFOLLOW) of the root and openat(O_DIRECTORY|
//     O_NOFOLLOW) of .haft: winfs.OpenDir and Dir.OpenDir; a symbolic link or
//     junction is ELOOP (G1) and every child effect is relative to the held
//     handles (G2).
//   - openat(O_NOFOLLOW|O_NONBLOCK) of the carrier and the lock: Dir.OpenFile.
//     Windows has no FIFOs in the filesystem, so O_NONBLOCK has nothing to
//     guard; O_CREAT|O_EXCL is an exclusive create (G3) and mode 0600 maps to
//     winfs.Private (explicit owner and protected owner-only DACL, G6 2a).
//   - fstat, lstat(path) and fstatat(AT_SYMLINK_NOFOLLOW): winfs.Fstat,
//     winfs.Lstat and Dir.Lstat; S_IFREG and S_IFDIR are Info.IsRegular and
//     Info.IsDir, and sameIdentity compares winfs.FileID (O5).
//   - linkat (install without replace), renameat (CAS replace) and unlinkat:
//     Dir.LinkIfAbsent, Dir.Rename and Dir.Remove. On a volume without hard
//     links LinkIfAbsent renames the stage without replacement (winfs.Moved);
//     the stage name is then already gone and is not unlinked (G4).
//   - flock(LOCK_EX), flock(LOCK_SH), flock(LOCK_UN): winfs.LockFile with
//     ExclusiveLock or SharedLock and winfs.UnlockFile on the content-free lock
//     file (G5).
//   - fsync of the .haft directory: Dir.Sync. The post-linearization sync
//     effect keeps its Unix signature, func(*os.File) error, and receives a
//     second handle of the held .haft directory opened through it with write
//     access (Dir.OpenForSync), because FlushFileBuffers needs write access on
//     a directory (ADR-092 P-c); its errors reach the caller (G7).
//
// The descriptor fields keep the values checkedFileDescriptor returns for the
// held handles; they mark the capability as valid, as on Unix. Effects never
// use them as handles: they go through the held winfs.Dir values, which keep
// a reference on the handle while a call runs. The unlinkStage effect keeps
// its Unix signature for the fault-injection tests; its production form
// removes the stage relative to the held .haft directory it was made for.

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
	"strings"

	"github.com/m0n0x41d/haft/internal/winfs"
)

const (
	lockFileName          = ".project-typeenv-genesis-review-lock"
	stagePrefix           = ".project-typeenv-genesis-review-stage-"
	maximumStaleStageDebt = 64
)

type heldDirectory struct {
	rootPath          string
	root              *winfs.Dir
	rootDescriptor    int
	haft              *winfs.Dir
	haftDescriptor    int
	lock              *os.File
	lockDescriptor    int
	postLinearization postLinearizationEffects
}

type observedTarget struct {
	carrier Carrier
	stat    winfs.Info
}

type postLinearizationEffects struct {
	unlinkStage   func(int, string) error
	syncDirectory func(*os.File) error
	rereadTarget  func(*heldDirectory) (observedTarget, error)
}

func productionPostLinearizationEffects(haft *winfs.Dir) postLinearizationEffects {
	return postLinearizationEffects{
		unlinkStage: func(_ int, name string) error {
			return haft.Remove(name)
		},
		syncDirectory: func(directory *os.File) error {
			return directory.Sync()
		},
		rereadTarget: func(directory *heldDirectory) (observedTarget, error) {
			return directory.readTarget()
		},
	}
}

// Install creates the canonical carrier without replacing different bytes.
func Install(projectRoot string, proposed Carrier) (InstallationResult, error) {
	if err := proposed.valid(); err != nil {
		return nil, err
	}
	directory, err := openHeldDirectoryForMutation(projectRoot)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	if err := directory.lockExclusive(); err != nil {
		return nil, err
	}
	defer directory.unlock()
	if err := directory.reconcileStaleStages(); err != nil {
		return nil, err
	}
	return directory.installAbsent(proposed)
}

// Replace installs proposed only when the current exact-byte digest matches
// expected. An already installed proposed carrier is an idempotent reuse.
func Replace(
	projectRoot string,
	expected Digest,
	proposed Carrier,
) (InstallationResult, error) {
	if !expected.valid() {
		return nil, fmt.Errorf("expected Genesis review digest is invalid")
	}
	if err := proposed.valid(); err != nil {
		return nil, err
	}
	directory, err := openHeldDirectoryForMutation(projectRoot)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	if err := directory.lockExclusive(); err != nil {
		return nil, err
	}
	defer directory.unlock()
	if err := directory.reconcileStaleStages(); err != nil {
		return nil, err
	}
	return directory.replaceMatching(expected, proposed)
}

// Read observes the canonical carrier without creating filesystem entries.
func Read(projectRoot string) (Carrier, error) {
	directory, err := openHeldDirectoryForRead(projectRoot)
	if err != nil {
		return Carrier{}, err
	}
	defer directory.Close()
	if err := directory.lockShared(); err != nil {
		return Carrier{}, err
	}
	defer directory.unlock()
	observed, err := directory.readTarget()
	if err != nil {
		return Carrier{}, err
	}
	return observed.carrier, nil
}

func openHeldDirectoryForMutation(projectRoot string) (*heldDirectory, error) {
	directory, err := openHeldDirectory(projectRoot, openOrCreateLockFileAt)
	if err != nil {
		return nil, err
	}
	if _, err := directory.haft.Sync(); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("sync Genesis review .haft directory: %w", err)
	}
	return directory, nil
}

func openHeldDirectoryForRead(projectRoot string) (*heldDirectory, error) {
	return openHeldDirectory(projectRoot, openExistingLockFileAt)
}

func openHeldDirectory(
	projectRoot string,
	openLock func(*winfs.Dir) (*os.File, error),
) (*heldDirectory, error) {
	absoluteRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve Genesis review project root: %w", err)
	}
	root, err := openDirectoryNoFollow(absoluteRoot)
	if err != nil {
		return nil, err
	}
	rootDescriptor, err := checkedFileDescriptor(root.File(), "Genesis review project root")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	haft, err := openDirectoryAt(root, DirectoryName)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open held Genesis review .haft directory: %w", err)
	}
	haftDescriptor, err := checkedFileDescriptor(haft.File(), "Genesis review .haft directory")
	if err != nil {
		_ = haft.Close()
		_ = root.Close()
		return nil, err
	}
	lock, err := openLock(haft)
	if err != nil {
		_ = haft.Close()
		_ = root.Close()
		return nil, err
	}
	lockDescriptor := -1
	if lock != nil {
		lockDescriptor, err = checkedFileDescriptor(lock, "Genesis review lock file")
		if err != nil {
			_ = lock.Close()
			_ = haft.Close()
			_ = root.Close()
			return nil, err
		}
	}
	directory := &heldDirectory{
		rootPath:          absoluteRoot,
		root:              root,
		rootDescriptor:    rootDescriptor,
		haft:              haft,
		haftDescriptor:    haftDescriptor,
		lock:              lock,
		lockDescriptor:    lockDescriptor,
		postLinearization: productionPostLinearizationEffects(haft),
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func (directory *heldDirectory) Close() error {
	if directory == nil {
		return nil
	}
	var lockErr error
	if directory.lock != nil {
		lockErr = directory.lock.Close()
		directory.lock = nil
		directory.lockDescriptor = -1
	}
	var haftErr error
	if directory.haft != nil {
		haftErr = directory.haft.Close()
		directory.haft = nil
		directory.haftDescriptor = -1
	}
	var rootErr error
	if directory.root != nil {
		rootErr = directory.root.Close()
		directory.root = nil
		directory.rootDescriptor = -1
	}
	return errors.Join(lockErr, haftErr, rootErr)
}

func (directory *heldDirectory) installAbsent(
	proposed Carrier,
) (InstallationResult, error) {
	if err := directory.requireAttachedIdentity(); err != nil {
		return nil, err
	}
	current, err := directory.readTarget()
	if err == nil {
		return directory.reuseOrConflict(
			current.carrier,
			proposed,
			MustBeAbsent{},
		)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stage, stageName, err := directory.writeStage(proposed)
	if err != nil {
		return nil, err
	}
	stagePresent := true
	defer func() {
		if stagePresent {
			_ = directory.haft.Remove(stageName)
		}
	}()
	if err := stage.Close(); err != nil {
		return nil, fmt.Errorf("close Genesis review stage: %w", err)
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return nil, err
	}
	linked, err := directory.haft.LinkIfAbsent(
		stageName,
		directory.haft,
		FileName,
	)
	if errors.Is(err, os.ErrExist) {
		current, readErr := directory.readTarget()
		if readErr != nil {
			return nil, readErr
		}
		return directory.reuseOrConflict(
			current.carrier,
			proposed,
			MustBeAbsent{},
		)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"atomically install Genesis review carrier without replacement: %w",
			err,
		)
	}
	if linked == winfs.Moved {
		// The volume has no hard links: the stage was renamed to the carrier
		// name without replacement, so there is no stage link left to remove.
		stagePresent = false
	}
	if stagePresent {
		err = directory.postLinearization.unlinkStage(
			directory.haftFD(),
			stageName,
		)
		if err != nil {
			stagePresent = false
			return outcomeUnknown(
				proposed,
				fmt.Sprintf(
					"remove installed Genesis review stage link: %v",
					err,
				),
				exactInstallRetry(proposed),
				PossibleOrphanStage{Name: stageName},
			), nil
		}
	}
	stagePresent = false
	return directory.finishInstallation(
		proposed,
		exactInstallRetry(proposed),
		PossibleOrphanStage{Name: stageName},
		NoKnownCleanupDebt{},
	), nil
}

func (directory *heldDirectory) replaceMatching(
	expected Digest,
	proposed Carrier,
) (InstallationResult, error) {
	if err := directory.requireAttachedIdentity(); err != nil {
		return nil, err
	}
	current, err := directory.readTarget()
	if errors.Is(err, os.ErrNotExist) {
		return Conflict{
			Current:     Missing{},
			Proposed:    proposed.Digest(),
			Expectation: MustMatch{Digest: expected},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if bytes.Equal(current.carrier.content, proposed.content) {
		return directory.finishReuse(
			proposed,
			exactReplaceRetry(expected, proposed),
		), nil
	}
	if current.carrier.Digest() != expected {
		return Conflict{
			Current:     Present{Carrier: current.carrier},
			Proposed:    proposed.Digest(),
			Expectation: MustMatch{Digest: expected},
		}, nil
	}
	stage, stageName, err := directory.writeStage(proposed)
	if err != nil {
		return nil, err
	}
	stagePresent := true
	defer func() {
		if stagePresent {
			_ = directory.haft.Remove(stageName)
		}
	}()
	if err := stage.Close(); err != nil {
		return nil, fmt.Errorf("close Genesis review stage: %w", err)
	}
	if err := directory.requireCurrentTarget(current); err != nil {
		latest, readErr := directory.readTarget()
		if errors.Is(readErr, os.ErrNotExist) {
			return Conflict{
				Current:     Missing{},
				Proposed:    proposed.Digest(),
				Expectation: MustMatch{Digest: expected},
			}, nil
		}
		if readErr != nil {
			return nil, errors.Join(err, readErr)
		}
		return Conflict{
			Current:     Present{Carrier: latest.carrier},
			Proposed:    proposed.Digest(),
			Expectation: MustMatch{Digest: expected},
		}, nil
	}
	if err := directory.haft.Rename(
		stageName,
		directory.haft,
		FileName,
	); err != nil {
		return nil, fmt.Errorf("atomically replace Genesis review carrier: %w", err)
	}
	stagePresent = false
	return directory.finishInstallation(
		proposed,
		exactReplaceRetry(expected, proposed),
		PossibleOrphanStage{Name: stageName},
		NoKnownCleanupDebt{},
	), nil
}

func (directory *heldDirectory) writeStage(
	carrier Carrier,
) (*os.File, string, error) {
	stageName, err := randomStageName()
	if err != nil {
		return nil, "", err
	}
	stage, err := openExclusiveFileAt(directory.haft, stageName)
	if err != nil {
		return nil, "", err
	}
	written, err := stage.Write(carrier.content)
	if err != nil {
		_ = stage.Close()
		_ = directory.haft.Remove(stageName)
		return nil, "", fmt.Errorf("write Genesis review stage: %w", err)
	}
	if written != len(carrier.content) {
		_ = stage.Close()
		_ = directory.haft.Remove(stageName)
		return nil, "", io.ErrShortWrite
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		_ = directory.haft.Remove(stageName)
		return nil, "", fmt.Errorf("sync Genesis review stage: %w", err)
	}
	return stage, stageName, nil
}

func (directory *heldDirectory) reconcileStaleStages() error {
	if err := directory.requireAttachedIdentity(); err != nil {
		return err
	}
	entries, err := directory.haft.File().ReadDir(-1)
	if err != nil {
		return fmt.Errorf("enumerate Genesis review stage debt: %w", err)
	}
	stageNames := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !validStageName(name) {
			continue
		}
		stat, err := directory.haft.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect Genesis review stage debt: %w", err)
		}
		if !stat.IsRegular() {
			return fmt.Errorf(
				"genesis review stage debt %s is not a regular file",
				name,
			)
		}
		stageNames = append(stageNames, name)
	}
	if len(stageNames) > maximumStaleStageDebt {
		return fmt.Errorf(
			"genesis review stage debt has %d entries; bounded cleanup permits %d",
			len(stageNames),
			maximumStaleStageDebt,
		)
	}
	for _, name := range stageNames {
		if err := directory.haft.Remove(name); err != nil {
			return fmt.Errorf("remove Genesis review stage debt: %w", err)
		}
	}
	if len(stageNames) == 0 {
		return nil
	}
	if _, err := directory.haft.Sync(); err != nil {
		return fmt.Errorf("sync Genesis review stage-debt cleanup: %w", err)
	}
	return directory.requireAttachedIdentity()
}

func (directory *heldDirectory) finishInstallation(
	proposed Carrier,
	retry ExactSameProposalRetry,
	cleanupBeforeSync CleanupDisposition,
	cleanupAfterSync CleanupDisposition,
) InstallationResult {
	if err := directory.syncHaftThroughEffect(); err != nil {
		return outcomeUnknown(
			proposed,
			fmt.Sprintf("sync Genesis review .haft directory: %v", err),
			retry,
			cleanupBeforeSync,
		)
	}
	installed, err := directory.postLinearization.rereadTarget(directory)
	if err != nil {
		return outcomeUnknown(
			proposed,
			fmt.Sprintf("reread Genesis review carrier: %v", err),
			retry,
			cleanupAfterSync,
		)
	}
	if !bytes.Equal(installed.carrier.content, proposed.content) {
		return outcomeUnknown(
			proposed,
			"reread Genesis review carrier differs from installed bytes",
			retry,
			cleanupAfterSync,
		)
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return outcomeUnknown(
			proposed,
			fmt.Sprintf(
				"revalidate Genesis review directory after installation: %v",
				err,
			),
			retry,
			cleanupAfterSync,
		)
	}
	return Created{Carrier: installed.carrier}
}

func (directory *heldDirectory) finishReuse(
	proposed Carrier,
	retry ExactSameProposalRetry,
) InstallationResult {
	cleanup := NoKnownCleanupDebt{}
	if err := directory.syncHaftThroughEffect(); err != nil {
		return outcomeUnknown(
			proposed,
			fmt.Sprintf(
				"sync exact Genesis review reuse directory: %v",
				err,
			),
			retry,
			cleanup,
		)
	}
	installed, err := directory.postLinearization.rereadTarget(directory)
	if err != nil {
		return outcomeUnknown(
			proposed,
			fmt.Sprintf("reread exact Genesis review reuse: %v", err),
			retry,
			cleanup,
		)
	}
	if !bytes.Equal(installed.carrier.content, proposed.content) {
		return outcomeUnknown(
			proposed,
			"reread exact Genesis review reuse differs from proposed bytes",
			retry,
			cleanup,
		)
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return outcomeUnknown(
			proposed,
			fmt.Sprintf(
				"revalidate exact Genesis review reuse: %v",
				err,
			),
			retry,
			cleanup,
		)
	}
	return Reused{Carrier: installed.carrier}
}

// syncHaftThroughEffect runs the syncDirectory effect, which the Unix code
// calls with the held .haft descriptor, on a write-access handle of the same
// held directory (Dir.OpenForSync, no name lookup).
func (directory *heldDirectory) syncHaftThroughEffect() error {
	file, err := directory.haft.OpenForSync()
	if err != nil {
		return err
	}
	syncErr := directory.postLinearization.syncDirectory(file)
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func outcomeUnknown(
	proposed Carrier,
	failure string,
	retry ExactSameProposalRetry,
	cleanup CleanupDisposition,
) OutcomeUnknown {
	digest := proposed.Digest()
	return OutcomeUnknown{
		Proposed: digest,
		Failure:  failure,
		Retry:    retry,
		Cleanup:  cleanup,
	}
}

func exactInstallRetry(proposed Carrier) ExactInstallRetry {
	return ExactInstallRetry{Proposed: proposed.Digest()}
}

func exactReplaceRetry(
	expected Digest,
	proposed Carrier,
) ExactReplaceRetry {
	return ExactReplaceRetry{
		Expected: expected,
		Proposed: proposed.Digest(),
	}
}

func (directory *heldDirectory) readTarget() (observedTarget, error) {
	if err := directory.requireAttachedIdentity(); err != nil {
		return observedTarget{}, err
	}
	file, stat, err := openReadOnlyRegularFileAt(directory.haft, FileName)
	if err != nil {
		return observedTarget{}, err
	}
	defer file.Close()
	content, err := readBounded(file, stat)
	if err != nil {
		return observedTarget{}, err
	}
	if err := directory.requireTargetIdentity(file, stat); err != nil {
		return observedTarget{}, err
	}
	carrier, err := NewCarrier(content)
	if err != nil {
		return observedTarget{}, err
	}
	return observedTarget{
		carrier: carrier,
		stat:    stat,
	}, nil
}

func (directory *heldDirectory) requireCurrentTarget(
	current observedTarget,
) error {
	if err := directory.requireAttachedIdentity(); err != nil {
		return err
	}
	attached, err := directory.haft.Lstat(FileName)
	if err != nil {
		return err
	}
	if !attached.IsRegular() {
		return fmt.Errorf("genesis review carrier is not a regular file")
	}
	if !sameIdentity(current.stat, attached) {
		return fmt.Errorf("genesis review carrier changed during CAS replacement")
	}
	return nil
}

func (directory *heldDirectory) requireTargetIdentity(
	file *os.File,
	opened winfs.Info,
) error {
	if _, err := checkedFileDescriptor(file, "Genesis review carrier"); err != nil {
		return err
	}
	held, err := winfs.Fstat(file)
	if err != nil {
		return fmt.Errorf("inspect held Genesis review carrier: %w", err)
	}
	attached, err := directory.haft.Lstat(FileName)
	if err != nil {
		return fmt.Errorf("inspect attached Genesis review carrier: %w", err)
	}
	if !attached.IsRegular() {
		return fmt.Errorf("genesis review carrier is not a regular file")
	}
	if !sameIdentity(opened, held) || !sameIdentity(held, attached) {
		return fmt.Errorf("genesis review carrier identity changed while reading")
	}
	return nil
}

func (directory *heldDirectory) requireAttachedIdentity() error {
	if err := directory.valid(); err != nil {
		return err
	}
	heldRoot, err := directory.root.Stat()
	if err != nil {
		return fmt.Errorf("inspect held Genesis review project root: %w", err)
	}
	pathRoot, err := winfs.Lstat(directory.rootPath)
	if err != nil {
		return fmt.Errorf("inspect Genesis review project-root path: %w", err)
	}
	if !pathRoot.IsDir() {
		return fmt.Errorf("genesis review project root is not a real directory")
	}
	if !sameIdentity(heldRoot, pathRoot) {
		return fmt.Errorf("genesis review project-root path identity changed")
	}
	heldHaft, err := directory.haft.Stat()
	if err != nil {
		return fmt.Errorf("inspect held Genesis review .haft directory: %w", err)
	}
	attachedHaft, err := directory.root.Lstat(DirectoryName)
	if err != nil {
		return fmt.Errorf("inspect attached Genesis review .haft directory: %w", err)
	}
	if !attachedHaft.IsDir() {
		return fmt.Errorf("genesis review .haft entry is not a real directory")
	}
	if !sameIdentity(heldHaft, attachedHaft) {
		return fmt.Errorf("genesis review .haft directory identity changed")
	}
	if err := directory.requireAttachedLockIdentity(); err != nil {
		return err
	}
	return nil
}

func (directory *heldDirectory) requireAttachedLockIdentity() error {
	if directory.lock == nil {
		return nil
	}
	heldLock, err := winfs.Fstat(directory.lock)
	if err != nil {
		return fmt.Errorf("inspect held Genesis review lock file: %w", err)
	}
	if !heldLock.IsRegular() {
		return fmt.Errorf("held Genesis review lock is not a regular file")
	}
	attachedLock, err := directory.haft.Lstat(lockFileName)
	if err != nil {
		return fmt.Errorf("inspect attached Genesis review lock file: %w", err)
	}
	if !attachedLock.IsRegular() {
		return fmt.Errorf("attached Genesis review lock is not a regular file")
	}
	if !sameIdentity(heldLock, attachedLock) {
		return fmt.Errorf("genesis review lock-file identity changed")
	}
	return nil
}

func (directory *heldDirectory) valid() error {
	if directory == nil ||
		directory.rootPath == "" ||
		directory.root == nil ||
		directory.rootDescriptor < 0 ||
		directory.haft == nil ||
		directory.haftDescriptor < 0 ||
		(directory.lock != nil && directory.lockDescriptor < 0) {
		return fmt.Errorf("genesis review directory capability is invalid")
	}
	return nil
}

func (directory *heldDirectory) haftFD() int {
	return directory.haftDescriptor
}

func (directory *heldDirectory) lockExclusive() error {
	if directory.lock == nil {
		return fmt.Errorf("genesis review mutation lock is unavailable")
	}
	if err := directory.requireAttachedIdentity(); err != nil {
		return err
	}
	if err := winfs.LockFile(directory.lock, winfs.ExclusiveLock); err != nil {
		return fmt.Errorf("lock Genesis review carrier exclusively: %w", err)
	}
	return directory.requireAttachedIdentity()
}

func (directory *heldDirectory) lockShared() error {
	if err := directory.requireAttachedIdentity(); err != nil {
		return err
	}
	if directory.lock == nil {
		return nil
	}
	if err := winfs.LockFile(directory.lock, winfs.SharedLock); err != nil {
		return fmt.Errorf("lock Genesis review carrier for reading: %w", err)
	}
	return directory.requireAttachedIdentity()
}

func (directory *heldDirectory) unlock() {
	if directory == nil || directory.lock == nil {
		return
	}
	_ = winfs.UnlockFile(directory.lock)
}

func (directory *heldDirectory) reuseOrConflict(
	current Carrier,
	proposed Carrier,
	expectation Expectation,
) (InstallationResult, error) {
	if bytes.Equal(current.content, proposed.content) {
		return directory.finishReuse(
			proposed,
			exactInstallRetry(proposed),
		), nil
	}
	return Conflict{
		Current:     Present{Carrier: current},
		Proposed:    proposed.Digest(),
		Expectation: expectation,
	}, nil
}

func openDirectoryNoFollow(path string) (*winfs.Dir, error) {
	directory, err := winfs.OpenDir(path)
	if err != nil {
		return nil, fmt.Errorf(
			"open Genesis review project root without following symlinks: %w",
			err,
		)
	}
	return directory, nil
}

func openDirectoryAt(parent *winfs.Dir, name string) (*winfs.Dir, error) {
	return parent.OpenDir(name)
}

func openOrCreateLockFileAt(parent *winfs.Dir) (*os.File, error) {
	file, err := openExistingRegularLockAt(parent, os.O_RDWR)
	if err == nil {
		return file, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	created, err := parent.OpenFile(
		lockFileName,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		winfs.Private,
	)
	if errors.Is(err, os.ErrExist) {
		return openExistingRegularLockAt(parent, os.O_RDWR)
	}
	if err != nil {
		return nil, fmt.Errorf("create Genesis review lock file: %w", err)
	}
	if err := requireOpenedRegularLock(parent, created); err != nil {
		_ = created.Close()
		return nil, err
	}
	return created, nil
}

func openExistingLockFileAt(parent *winfs.Dir) (*os.File, error) {
	file, err := openExistingRegularLockAt(parent, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return file, err
}

func openExistingRegularLockAt(
	parent *winfs.Dir,
	accessFlags int,
) (*os.File, error) {
	entry, err := parent.Lstat(lockFileName)
	if err != nil {
		return nil, err
	}
	if !entry.IsRegular() {
		return nil, fmt.Errorf("genesis review lock entry is not a regular file")
	}
	file, err := parent.OpenFile(lockFileName, accessFlags, winfs.Inherited)
	if err != nil {
		return nil, fmt.Errorf("open existing Genesis review lock file: %w", err)
	}
	if err := requireOpenedRegularLock(parent, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func requireOpenedRegularLock(parent *winfs.Dir, file *os.File) error {
	if _, err := checkedFileDescriptor(file, "Genesis review lock file"); err != nil {
		return err
	}
	held, err := winfs.Fstat(file)
	if err != nil {
		return fmt.Errorf("inspect held Genesis review lock file: %w", err)
	}
	if !held.IsRegular() {
		return fmt.Errorf("held Genesis review lock is not a regular file")
	}
	attached, err := parent.Lstat(lockFileName)
	if err != nil {
		return fmt.Errorf("inspect attached Genesis review lock file: %w", err)
	}
	if !attached.IsRegular() {
		return fmt.Errorf("attached Genesis review lock is not a regular file")
	}
	if !sameIdentity(held, attached) {
		return fmt.Errorf("genesis review lock-file identity changed while opening")
	}
	return nil
}

func openExclusiveFileAt(parent *winfs.Dir, name string) (*os.File, error) {
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Private)
	if err != nil {
		return nil, fmt.Errorf("create exclusive Genesis review stage: %w", err)
	}
	return file, nil
}

func openReadOnlyRegularFileAt(
	parent *winfs.Dir,
	name string,
) (*os.File, winfs.Info, error) {
	entry, err := parent.Lstat(name)
	if err != nil {
		return nil, winfs.Info{}, err
	}
	if !entry.IsRegular() {
		return nil, winfs.Info{}, fmt.Errorf(
			"genesis review carrier is not a regular file",
		)
	}
	file, err := parent.OpenFile(name, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		return nil, winfs.Info{}, err
	}
	stat, err := winfs.Fstat(file)
	if err != nil {
		_ = file.Close()
		return nil, winfs.Info{}, err
	}
	if !stat.IsRegular() {
		_ = file.Close()
		return nil, winfs.Info{}, fmt.Errorf(
			"genesis review carrier is not a regular file",
		)
	}
	return file, stat, nil
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
		return 0, fmt.Errorf("%s descriptor exceeds the platform int range", subject)
	}
	return int(descriptor), nil
}

func readBounded(file *os.File, stat winfs.Info) ([]byte, error) {
	if stat.Size > MaximumBytes {
		return nil, fmt.Errorf(
			"genesis review carrier exceeds %d bytes",
			MaximumBytes,
		)
	}
	content, err := io.ReadAll(io.LimitReader(file, MaximumBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Genesis review carrier: %w", err)
	}
	if len(content) > MaximumBytes {
		return nil, fmt.Errorf(
			"genesis review carrier exceeds %d bytes",
			MaximumBytes,
		)
	}
	return content, nil
}

func randomStageName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate Genesis review stage name: %w", err)
	}
	return stagePrefix + hex.EncodeToString(random[:]), nil
}

func validStageName(name string) bool {
	suffix, found := strings.CutPrefix(name, stagePrefix)
	if !found || len(suffix) != 32 {
		return false
	}
	for _, character := range []byte(suffix) {
		decimal := character >= '0' && character <= '9'
		lowerHex := character >= 'a' && character <= 'f'
		if !decimal && !lowerHex {
			return false
		}
	}
	return true
}

func sameIdentity(left winfs.Info, right winfs.Info) bool {
	return left.ID == right.ID
}
