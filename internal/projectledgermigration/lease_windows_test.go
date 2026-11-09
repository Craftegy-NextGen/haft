//go:build windows

package projectledgermigration

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/winfs"
	"golang.org/x/sys/windows"
)

const (
	leaseHelperDirectoryEnv = "HAFT_PLM_LEASE_HELPER_DIRECTORY"
	leaseHelperReady        = "PLM_LEASE_HELPER_READY"
	leaseHelperFailed       = "PLM_LEASE_HELPER_FAILED"
)

// leaseHelperHeld keeps the helper's lease reachable until the process
// exits; the helper never releases it, so only the kernel's cleanup at
// process exit does.
var leaseHelperHeld migrationFileLease

// TestMigrationLeaseHelperProcess is the body of the helper process that the
// two-process tests start by re-executing the test binary. It acquires the
// migration file lease, reports ready and holds it until its stdin closes.
func TestMigrationLeaseHelperProcess(t *testing.T) {
	directory := os.Getenv(leaseHelperDirectoryEnv)
	if directory == "" {
		t.Skip("entry point for re-executed helper processes; runs only with " + leaseHelperDirectoryEnv + " set")
	}
	lease, acquired, err := tryAcquireMigrationFileLease(directory, migrationLeaseFilename)
	if err != nil || !acquired {
		fmt.Println(leaseHelperFailed, acquired, err)
		return
	}
	leaseHelperHeld = lease
	fmt.Println(leaseHelperReady)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// startLeaseHelper starts a helper process that holds the migration lease in
// directory and waits until it reports ready. The returned function closes
// the helper's stdin and waits for it to exit without releasing the lease.
func startLeaseHelper(t *testing.T, directory string) func() {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrationLeaseHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), leaseHelperDirectoryEnv+"="+directory)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	done := make(chan struct{})
	ready := make(chan error, 1)
	go func() {
		defer close(done)
		reported := false
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case reported:
			case line == leaseHelperReady:
				reported = true
				ready <- nil
			case strings.HasPrefix(line, leaseHelperFailed):
				reported = true
				ready <- errors.New(line)
			}
		}
		if !reported {
			ready <- errors.New("helper exited before it reported ready")
		}
	}()
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = cmd.Process.Kill()
			_ = stdin.Close()
			<-done
			_ = cmd.Wait()
		}
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("migration lease helper: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("migration lease helper did not report ready within 60s")
	}
	return func() {
		t.Helper()
		_ = stdin.Close()
		<-done
		exited = true
		if err := cmd.Wait(); err != nil {
			t.Fatalf("migration lease helper exit: %v", err)
		}
	}
}

func leaseTestDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

// A migration lease held by another process is busy: (nil, false, nil), the
// result of EWOULDBLOCK in lease_supported.go. The coordinator keeps retrying
// and reports ErrMigrationLeaseTimeout at its deadline. After the holder
// exits, the lease is free again.
func TestMigrationFileLeaseReportsBusyAcrossProcesses(t *testing.T) {
	directory := leaseTestDirectory(t)
	exitHelper := startLeaseHelper(t, directory)

	lease, acquired, err := tryAcquireMigrationFileLease(directory, migrationLeaseFilename)
	if err != nil || acquired || lease != nil {
		if lease != nil {
			_ = lease.release()
		}
		t.Fatalf("tryAcquireMigrationFileLease under contention = %v, %t, %v; want nil, false, nil", lease, acquired, err)
	}

	coordinator := &migrationCoordinator{directory: directory, state: newMigrationProcessState()}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	held, waited, err := coordinator.acquire(ctx)
	if !errors.Is(err, ErrMigrationLeaseTimeout) || held != nil {
		t.Fatalf("coordinator acquire under contention = %v, %v; want ErrMigrationLeaseTimeout", held, err)
	}
	if waited < 250*time.Millisecond {
		t.Fatalf("coordinator gave up after %s, before its deadline", waited)
	}

	exitHelper()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, _, err = coordinator.acquire(ctx)
	if err != nil {
		t.Fatalf("coordinator acquire after the holder exited: %v", err)
	}
	if err := held.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// Two handles in one process exclude each other, as two flock open file
// descriptions do on Unix; release makes the lease available again and is
// idempotent.
func TestMigrationFileLeaseReportsBusyWithinProcess(t *testing.T) {
	directory := leaseTestDirectory(t)
	first, acquired, err := tryAcquireMigrationFileLease(directory, migrationLeaseFilename)
	if err != nil || !acquired {
		t.Fatalf("first tryAcquireMigrationFileLease = %t, %v", acquired, err)
	}
	second, acquired, err := tryAcquireMigrationFileLease(directory, migrationLeaseFilename)
	if err != nil || acquired || second != nil {
		t.Fatalf("second tryAcquireMigrationFileLease = %v, %t, %v; want nil, false, nil", second, acquired, err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	third, acquired, err := tryAcquireMigrationFileLease(directory, migrationLeaseFilename)
	if err != nil || !acquired {
		t.Fatalf("tryAcquireMigrationFileLease after release = %t, %v", acquired, err)
	}
	_ = third.release()
}

// The Windows meaning of safeMigrationLeaseMetadata (ADR-092 O2, 2a): a lease
// created by tryAcquireMigrationFileLease is private (token user owner,
// protected owner-only DACL, one link).
func TestMigrationFileLeaseIsPrivate(t *testing.T) {
	directory := leaseTestDirectory(t)
	lease, acquired, err := tryAcquireMigrationFileLease(directory, migrationLeaseFilename)
	if err != nil || !acquired {
		t.Fatalf("tryAcquireMigrationFileLease = %t, %v", acquired, err)
	}
	defer lease.release()
	held, ok := lease.(*windowsMigrationFileLease)
	if !ok || held.file == nil {
		t.Fatalf("lease = %T, want *windowsMigrationFileLease with a file", lease)
	}
	if err := winfs.CheckPrivate(held.file); err != nil {
		t.Fatalf("migration lease is not private: %v", err)
	}
}

// Unsafe existing carriers are rejected before they are locked, as in the
// Unix TestEnsureCurrentForServeRejectsUnsafeLeaseCarrier: a file with
// inherited security (the Windows counterpart of the 0644 case), a symbolic
// link and a hard-linked file.
func TestMigrationFileLeaseRejectsUnsafeCarriers(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{
			name: "inherited security",
			prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "project ledger migration lease ownership or mode is unsafe",
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, path string) {
				target := filepath.Join(filepath.Dir(path), "lease-target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				err := os.Symlink(target, path)
				if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
					t.Skipf("creating a symbolic link needs SeCreateSymbolicLinkPrivilege or Developer Mode: %v", err)
				}
				if err != nil {
					t.Fatal(err)
				}
			},
			want: "open project ledger migration lease without following symlinks",
		},
		{
			name: "hard linked",
			prepare: func(t *testing.T, path string) {
				lease, acquired, err := tryAcquireMigrationFileLease(filepath.Dir(path), filepath.Base(path))
				if err != nil || !acquired {
					t.Fatalf("create private lease: %t, %v", acquired, err)
				}
				if err := lease.release(); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(path, filepath.Join(filepath.Dir(path), "second-name")); err != nil {
					t.Fatal(err)
				}
			},
			want: "project ledger migration lease ownership or mode is unsafe",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := leaseTestDirectory(t)
			test.prepare(t, filepath.Join(directory, migrationLeaseFilename))
			lease, acquired, err := tryAcquireMigrationFileLease(directory, migrationLeaseFilename)
			if err == nil || acquired || lease != nil {
				t.Fatalf("unsafe lease = %v, %t, %v; want an error", lease, acquired, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe lease error = %v, want %q", err, test.want)
			}
		})
	}
}

// The lease directory itself is not followed through a junction
// (open(O_DIRECTORY|O_NOFOLLOW)).
func TestMigrationFileLeaseRejectsJunctionDirectory(t *testing.T) {
	root := leaseTestDirectory(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("create junction: %v: %s", err, out)
	}
	lease, acquired, err := tryAcquireMigrationFileLease(junction, migrationLeaseFilename)
	if err == nil || acquired || lease != nil {
		t.Fatalf("lease through a junction = %v, %t, %v; want an error", lease, acquired, err)
	}
	if !strings.Contains(err.Error(), "open project ledger directory without following symlinks") {
		t.Fatalf("lease through a junction error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, migrationLeaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease was created through the junction: %v", err)
	}
}
