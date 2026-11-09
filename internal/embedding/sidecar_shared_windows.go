//go:build windows

// winport: mirrors internal/embedding/sidecar_shared_os_unix.go sha256:83e7a45dee8f32309f56e666515d58146f7434cfe3495dd998b8c15aba25578c (blob 88029565082a4c7143d249e6d33d9735332feb13)

package embedding

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/m0n0x41d/haft/internal/winfs"
	"github.com/m0n0x41d/haft/logger"
)

// The Windows pieces of the shared embedding sidecar (ADR-095 option A, ADR-091
// R-b). Upstream's sidecar_shared_unix.go keeps the dial, handshake and
// request code for every OS; the four OS-specific pieces live here and in
// sidecar_shared_os_unix.go: the start lock, the daemon start, the socket
// directory and its owner check.
//
// The transport is AF_UNIX: Go's net package dials "unix" on Windows 10 1803
// and later. Windows AF_UNIX has no peer-credential check, so the endpoint is
// protected by its directory alone: a private directory (explicit owner and a
// protected DACL for the user, SYSTEM and Administrators only, ADR-092 O2)
// that CheckPrivate verifies on every use.

// maxSocketPathBytes is the longest AF_UNIX socket path Windows accepts:
// sockaddr_un.sun_path holds 108 bytes including the terminating NUL. The
// %LOCALAPPDATA% may be up to 68 bytes long before the default socket path
// (\haft\embed\embed-<16 hex digits>.sock) reaches it.
const maxSocketPathBytes = 107

// sharedDaemonCreationFlags detaches the daemon from the starting process: a
// new process group so it ignores the starter's Ctrl+C and Ctrl+Break, and no
// console of its own to inherit. The daemon is deliberately not started
// through winproc: a winproc job is kill-on-close, and the shared daemon must
// outlive the haft process that started it, as a Setpgid group outlives its
// leader on Unix. A daemon started inside a kill-on-close job of the caller
// stays in it, because breakaway needs the job's permission.
const sharedDaemonCreationFlags = windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS

func withSharedSidecarLock(
	lockPath string,
	fn func() (*sharedSidecarAdapter, error),
) (*sharedSidecarAdapter, error) {
	file, err := winfs.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, winfs.Private)
	if err != nil {
		return nil, fmt.Errorf("open shared sidecar lock: %w", err)
	}
	defer file.Close()
	if err := winfs.CheckPrivate(file); err != nil {
		return nil, fmt.Errorf("shared sidecar lock: %w", err)
	}

	if err := winfs.LockFile(file, winfs.ExclusiveLock); err != nil {
		return nil, fmt.Errorf("lock shared sidecar: %w", err)
	}
	defer func() { _ = winfs.UnlockFile(file) }()

	return fn()
}

func startSharedSidecarDaemon(
	binary string,
	spec sidecarSpec,
	socket string,
) (sharedDaemonStart, error) {
	if len(socket) > maxSocketPathBytes {
		// Not wrapped in errSharedSidecarUnusable: the binary is fine, so
		// haft falls back to a per-process stdio sidecar as ADR-095 says.
		return sharedDaemonStart{}, fmt.Errorf(
			"shared sidecar socket path is %d bytes, over the AF_UNIX limit of %d: %s",
			len(socket),
			maxSocketPathBytes,
			socket,
		)
	}

	args := append([]string{}, spec.Args...)
	args = append(args, "--serve-socket", socket)
	args = append(args, "--idle-timeout-secs", strconv.Itoa(sharedIdleTimeoutSecs()))

	cmd := exec.Command(binary, args...)
	cmd.Dir = filepath.Dir(socket)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: sharedDaemonCreationFlags}

	if err := cmd.Start(); err != nil {
		return sharedDaemonStart{}, fmt.Errorf("start shared sidecar: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err != nil {
			logger.Info().Err(err).Msg("shared embedding sidecar exited")
		}
		done <- err
	}()

	return sharedDaemonStart{cmd: cmd, done: done}, nil
}

// sharedSidecarDir returns the private socket directory: the HAFT_EMBED_SOCKET_DIR
// override, or %LOCALAPPDATA%\haft\embed (os.UserCacheDir on Windows).
func sharedSidecarDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(sharedSocketDirEnv)); override != "" {
		return ensurePrivateDir(override)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate shared sidecar directory: %w", err)
	}
	return ensurePrivateDir(filepath.Join(cache, "haft", "embed"))
}

// ensurePrivateDir creates dir when it is missing, with the private security
// descriptor, and verifies that it is private. Missing ancestors are created
// with the ordinary inherited descriptor: they live in the user's own profile
// (or wherever the override points), and only the directory that holds the
// socket and lock files carries the protection. An existing directory that is
// a link, is not a directory, or is not private is refused, never repaired:
// Windows has no chmod, and rewriting a foreign ACL would hide an intrusion.
func ensurePrivateDir(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve private dir %s: %w", dir, err)
	}
	parent := filepath.Dir(absolute)
	if parent != absolute {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return "", fmt.Errorf("create private dir %s: %w", absolute, err)
		}
		if err := createPrivateChild(parent, filepath.Base(absolute)); err != nil {
			return "", fmt.Errorf("create private dir %s: %w", absolute, err)
		}
	}
	if err := validatePrivateDir(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

func createPrivateChild(parent string, name string) error {
	held, err := winfs.OpenDir(parent)
	if err != nil {
		return err
	}
	defer held.Close()
	if err := held.Mkdir(name, winfs.Private); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// validatePrivateDir is the Windows meaning of the Unix check (mode 0700 and
// owner uid): OpenDir rejects a symbolic link or junction at the final
// component, and CheckPrivate requires the token user as owner and a protected
// DACL that allows nobody else.
func validatePrivateDir(dir string) error {
	held, err := winfs.OpenDir(dir)
	if err != nil {
		return fmt.Errorf("open private dir %s: %w", dir, err)
	}
	defer held.Close()
	info, err := held.File().Stat()
	if err != nil {
		return fmt.Errorf("stat private dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("private dir %s is not a directory", dir)
	}
	if err := winfs.CheckPrivate(held.File()); err != nil {
		return fmt.Errorf("private dir %s: %w", dir, err)
	}
	return nil
}
