package winfs

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	helperRoleEnv = "WINFS_TEST_HELPER_ROLE"
	helperPathEnv = "WINFS_TEST_HELPER_PATH"
	helperReady   = "WINFS_HELPER_READY"
	helperFailed  = "WINFS_HELPER_FAILED"
)

// helperHeld keeps the helper's lock carrier reachable, so no finalizer can
// close it before the process exits.
var helperHeld any

// TestHelperProcess is the body of the helper processes that the two-process
// contention tests start by re-executing the test binary. It takes the lock
// named by the role, reports ready, and holds the lock until its stdin
// closes. It then returns without unlocking or closing, so only the kernel's
// cleanup at process exit can release the lock.
func TestHelperProcess(t *testing.T) {
	role := os.Getenv(helperRoleEnv)
	if role == "" {
		t.Skip("entry point for re-executed helper processes; runs only with " + helperRoleEnv + " set")
	}
	held, err := takeHelperLock(role, os.Getenv(helperPathEnv))
	if err != nil {
		fmt.Println(helperFailed, err)
		return
	}
	helperHeld = held
	fmt.Println(helperReady)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func takeHelperLock(role string, path string) (any, error) {
	switch role {
	case "file-exclusive", "file-shared":
		file, err := OpenFile(path, os.O_RDWR|os.O_CREATE, Inherited)
		if err != nil {
			return nil, err
		}
		mode := ExclusiveLock
		if role == "file-shared" {
			mode = SharedLock
		}
		if err := LockFile(file, mode); err != nil {
			return nil, err
		}
		return file, nil
	case "dir":
		return TryLockDir(path)
	}
	return nil, fmt.Errorf("unknown helper role %q", role)
}

// helperProcess is a running helper that holds a lock.
type helperProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	output chan struct{}
	exited bool
}

// startHelper starts a helper process and waits until it holds the lock.
func startHelper(t *testing.T, role string, path string) *helperProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperRoleEnv+"="+role, helperPathEnv+"="+path)
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
	helper := &helperProcess{cmd: cmd, stdin: stdin, output: make(chan struct{})}
	ready := make(chan error, 1)
	go helper.readOutput(stdout, ready)
	t.Cleanup(func() {
		if !helper.exited {
			_ = helper.kill()
		}
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("helper %s: %v", role, err)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("helper %s did not report ready within 60s", role)
	}
	return helper
}

func (helper *helperProcess) readOutput(stdout io.Reader, ready chan<- error) {
	defer close(helper.output)
	reported := false
	report := func(err error) {
		if !reported {
			reported = true
			ready <- err
		}
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == helperReady:
			report(nil)
		case strings.HasPrefix(line, helperFailed):
			report(errors.New(line))
		}
	}
	report(errors.New("helper exited before it reported ready"))
}

// exit closes the helper's stdin, so it returns from its test and exits
// without releasing the lock itself.
func (helper *helperProcess) exit() error {
	_ = helper.stdin.Close()
	<-helper.output
	helper.exited = true
	return helper.cmd.Wait()
}

// kill terminates the helper (TerminateProcess).
func (helper *helperProcess) kill() error {
	_ = helper.cmd.Process.Kill()
	_ = helper.stdin.Close()
	<-helper.output
	helper.exited = true
	_ = helper.cmd.Wait()
	return nil
}

// eventually retries attempt until it succeeds or the timeout passes, and
// returns how long it took. Microsoft documents the release of a lock at
// process exit as prompt but not immediate.
func eventually(timeout time.Duration, attempt func() error) (time.Duration, error) {
	start := time.Now()
	for {
		err := attempt()
		if err == nil || time.Since(start) > timeout {
			return time.Since(start), err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// createJunction makes link a junction (mount point) to the absolute
// directory target with FSCTL_SET_REPARSE_POINT; junctions need no privilege.
func createJunction(t *testing.T, link string, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0o755); err != nil {
		t.Fatalf("create junction directory: %v", err)
	}
	handle, err := createFile(
		link,
		windows.GENERIC_WRITE,
		0,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		nil,
	)
	if err != nil {
		t.Fatalf("open junction directory: %v", err)
	}
	defer closeHandle(handle)
	substitute := utf16.Encode([]rune(`\??\` + target))
	printName := utf16.Encode([]rune(target))
	names := append(append(append(substitute, 0), printName...), 0)
	dataLength := 8 + 2*len(names)
	buffer := make([]byte, 8+dataLength)
	binary.LittleEndian.PutUint32(buffer[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(dataLength))
	binary.LittleEndian.PutUint16(buffer[8:], 0)
	binary.LittleEndian.PutUint16(buffer[10:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(2*(len(substitute)+1)))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(2*len(printName)))
	for index, unit := range names {
		binary.LittleEndian.PutUint16(buffer[16+2*index:], unit)
	}
	var returned uint32
	err = windows.DeviceIoControl(
		handle,
		windows.FSCTL_SET_REPARSE_POINT,
		&buffer[0],
		uint32(len(buffer)),
		nil,
		0,
		&returned,
		nil,
	)
	if err != nil {
		t.Fatalf("set junction reparse point: %v", err)
	}
}

// createSymlinkOrSkip creates a symbolic link, or skips the test when the
// process lacks SeCreateSymbolicLinkPrivilege and Developer Mode is off.
func createSymlinkOrSkip(t *testing.T, target string, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Skipf("creating a symbolic link needs SeCreateSymbolicLinkPrivilege or Developer Mode: %v", err)
	}
	if err != nil {
		t.Fatalf("create symbolic link: %v", err)
	}
}

// openTempDir returns an absolute temporary directory held as a Dir.
func openTempDir(t *testing.T) (*Dir, string) {
	t.Helper()
	path := t.TempDir()
	dir, err := OpenDir(path)
	if err != nil {
		t.Fatalf("OpenDir(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir, path
}

// writeFile creates name in dir with content through winfs.
func writeFile(t *testing.T, dir *Dir, name string, content string) {
	t.Helper()
	file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// volumeFilesystem names the filesystem of the volume that holds path.
func volumeFilesystem(path string) string {
	handle, err := createFile(
		path,
		windows.FILE_READ_ATTRIBUTES,
		shareAll,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		nil,
	)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	defer closeHandle(handle)
	name := make([]uint16, windows.MAX_PATH+1)
	err = windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, nil, &name[0], uint32(len(name)))
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	return windows.UTF16ToString(name) + " on " + filepath.VolumeName(path)
}
