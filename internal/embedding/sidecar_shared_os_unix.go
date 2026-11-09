//go:build !windows

package embedding

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/m0n0x41d/haft/logger"
)

func withSharedSidecarLock(
	lockPath string,
	fn func() (*sharedSidecarAdapter, error),
) (*sharedSidecarAdapter, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open shared sidecar lock: %w", err)
	}
	defer file.Close()
	_ = file.Chmod(0o600)

	if err := flockFile(file, syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock shared sidecar: %w", err)
	}
	defer func() { _ = flockFile(file, syscall.LOCK_UN) }()

	return fn()
}

func flockFile(file *os.File, how int) error {
	fd := file.Fd()
	if fd > (^uintptr(0) >> 1) {
		return fmt.Errorf("file descriptor %d exceeds int range", fd)
	}
	return syscall.Flock(int(fd), how) // #nosec G115 -- fd is range-checked above.
}

func startSharedSidecarDaemon(
	binary string,
	spec sidecarSpec,
	socket string,
) (sharedDaemonStart, error) {
	args := append([]string{}, spec.Args...)
	args = append(args, "--serve-socket", socket)
	args = append(args, "--idle-timeout-secs", strconv.Itoa(sharedIdleTimeoutSecs()))

	cmd := exec.Command(binary, args...)
	cmd.Dir = filepath.Dir(socket)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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

func sharedSidecarDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(sharedSocketDirEnv)); override != "" {
		return ensurePrivateDir(override)
	}
	parent := filepath.Join("/tmp", fmt.Sprintf("haft-%d", os.Getuid()))
	dir, err := ensurePrivateDir(parent)
	if err != nil {
		return "", err
	}
	return ensurePrivateDir(filepath.Join(dir, "embed"))
}

func ensurePrivateDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create private dir %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("stat private dir %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("private dir %s is a symlink", dir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("private dir %s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("chmod private dir %s: %w", dir, err)
	}
	if err := validatePrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func validatePrivateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat private dir %s: %w", dir, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private dir %s is accessible by group/other", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("private dir %s is not owned by current user", dir)
	}
	return nil
}
