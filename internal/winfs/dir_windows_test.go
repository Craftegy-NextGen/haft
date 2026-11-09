package winfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestOpenDirRejectsRelativePath(t *testing.T) {
	_, err := OpenDir("relative")
	if !errors.Is(err, ErrRelativePath) {
		t.Fatalf("OpenDir(relative) = %v, want ErrRelativePath", err)
	}
	_, err = OpenFile("relative", os.O_RDONLY, Inherited)
	if !errors.Is(err, ErrRelativePath) {
		t.Fatalf("OpenFile(relative) = %v, want ErrRelativePath", err)
	}
}

func TestOpenDirResolvesIntermediateComponents(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "via"), filepath.Join(root, "real"))
	dir, err := OpenDir(filepath.Join(root, "via", "child"))
	if err != nil {
		t.Fatalf("OpenDir through an intermediate junction: %v", err)
	}
	_ = dir.Close()
}

func TestOpenDirRejectsJunctionAtFinalComponent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "junction"), target)
	_, err := OpenDir(filepath.Join(root, "junction"))
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("OpenDir(junction) = %v, want ELOOP", err)
	}
	parent, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	_, err = parent.OpenDir("junction")
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("Dir.OpenDir(junction) = %v, want ELOOP", err)
	}
	_, err = parent.OpenFile("junction", os.O_RDONLY, Inherited)
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("Dir.OpenFile(junction) = %v, want ELOOP", err)
	}
	if _, err := os.Stat(filepath.Join(target)); err != nil {
		t.Fatalf("target must be untouched: %v", err)
	}
}

func TestOpenRejectsSymlinkAtFinalComponent(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target-dir")
	targetFile := filepath.Join(root, "target-file")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	createSymlinkOrSkip(t, targetDir, filepath.Join(root, "dir-link"))
	createSymlinkOrSkip(t, targetFile, filepath.Join(root, "file-link"))

	if _, err := OpenDir(filepath.Join(root, "dir-link")); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("OpenDir(symlink) = %v, want ELOOP", err)
	}
	if _, err := OpenFile(filepath.Join(root, "file-link"), os.O_RDONLY, Inherited); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("OpenFile(symlink) = %v, want ELOOP", err)
	}
	parent, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if _, err := parent.OpenDir("dir-link"); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("Dir.OpenDir(symlink) = %v, want ELOOP", err)
	}
	if _, err := parent.OpenFile("file-link", os.O_RDWR, Inherited); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("Dir.OpenFile(symlink) = %v, want ELOOP", err)
	}
	if _, err := parent.OpenFile("file-link", os.O_RDWR|os.O_CREATE, Inherited); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("Dir.OpenFile(symlink, O_CREATE) = %v, want ELOOP", err)
	}
	if _, err := parent.OpenFile("file-link", os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("exclusive create over a symlink = %v, want fs.ErrExist", err)
	}
	info, err := parent.Lstat("file-link")
	if err != nil || !info.IsNameSurrogate() || info.IsRegular() {
		t.Fatalf("Lstat(symlink) = %+v, %v; want a name surrogate", info, err)
	}
	if err := parent.Remove("file-link"); err != nil {
		t.Fatalf("Remove(symlink): %v", err)
	}
	if got := readFile(t, targetFile); got != "secret" {
		t.Fatalf("removing the link changed the target: %q", got)
	}
}

func TestOpenDirOnFileIsNotDirectory(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "file", "x")
	if _, err := OpenDir(filepath.Join(path, "file")); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("OpenDir(file) = %v, want ENOTDIR", err)
	}
	if _, err := dir.OpenDir("file"); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("Dir.OpenDir(file) = %v, want ENOTDIR", err)
	}
}

func TestDirOperationsRejectInvalidNames(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "good", "x")
	for _, name := range []string{"", ".", "..", `sub\file`, "sub/file", "good:stream"} {
		checks := map[string]error{}
		_, checks["OpenDir"] = dir.OpenDir(name)
		_, checks["OpenFile"] = dir.OpenFile(name, os.O_RDWR|os.O_CREATE, Inherited)
		checks["Mkdir"] = dir.Mkdir(name, Inherited)
		_, checks["Lstat"] = dir.Lstat(name)
		checks["Remove"] = dir.Remove(name)
		checks["Rename old"] = dir.Rename(name, dir, "other")
		checks["Rename new"] = dir.Rename("good", dir, name)
		checks["RenameNoReplace new"] = dir.RenameNoReplace("good", dir, name)
		_, checks["LinkIfAbsent new"] = dir.LinkIfAbsent("good", dir, name)
		for op, err := range checks {
			if !errors.Is(err, ErrInvalidName) {
				t.Errorf("%s(%q) = %v, want ErrInvalidName", op, name, err)
			}
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "good" {
		t.Fatalf("invalid names must not create entries, found %v", entries)
	}
}

func TestDirOpenFileExclusiveCreate(t *testing.T) {
	dir, path := openTempDir(t)
	file, err := dir.OpenFile("new", os.O_RDWR|os.O_CREATE|os.O_EXCL, Inherited)
	if err != nil {
		t.Fatalf("exclusive create: %v", err)
	}
	if _, err := file.WriteString("first"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	_, err = dir.OpenFile("new", os.O_RDWR|os.O_CREATE|os.O_EXCL, Inherited)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second exclusive create = %v, want fs.ErrExist", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != filepath.Join(path, "new") {
		t.Fatalf("error %v must be an *os.PathError naming the entry", err)
	}
	if got := readFile(t, filepath.Join(path, "new")); got != "first" {
		t.Fatalf("content after failed exclusive create = %q", got)
	}
}

func TestDirOpenFileModes(t *testing.T) {
	dir, path := openTempDir(t)
	if _, err := dir.OpenFile("missing", os.O_RDONLY, Inherited); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open missing = %v, want fs.ErrNotExist", err)
	}
	created, err := dir.OpenFile("file", os.O_RDWR|os.O_CREATE, Inherited)
	if err != nil {
		t.Fatalf("open-if create: %v", err)
	}
	_, _ = created.WriteString("hello")
	_ = created.Close()
	opened, err := dir.OpenFile("file", os.O_RDWR|os.O_CREATE, Inherited)
	if err != nil {
		t.Fatalf("open-if existing: %v", err)
	}
	content, _ := io.ReadAll(opened)
	_ = opened.Close()
	if string(content) != "hello" {
		t.Fatalf("open-if must not truncate, read %q", content)
	}
	appender, err := dir.OpenFile("file", os.O_WRONLY|os.O_APPEND, Inherited)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	_, _ = appender.WriteString(" world")
	_ = appender.Close()
	if got := readFile(t, filepath.Join(path, "file")); got != "hello world" {
		t.Fatalf("after append = %q", got)
	}
	truncated, err := dir.OpenFile("file", os.O_WRONLY|os.O_TRUNC|os.O_SYNC, Inherited)
	if err != nil {
		t.Fatalf("open truncate: %v", err)
	}
	_ = truncated.Close()
	if got := readFile(t, filepath.Join(path, "file")); got != "" {
		t.Fatalf("after truncate = %q", got)
	}
	if _, err := dir.OpenFile("file", os.O_RDWR|0x4000000, Inherited); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("unsupported flag = %v, want fs.ErrInvalid", err)
	}
}

func TestPathOpenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	file, err := OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited)
	if err != nil {
		t.Fatalf("exclusive create: %v", err)
	}
	_, _ = file.WriteString("x")
	_ = file.Close()
	if _, err := OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second exclusive create = %v, want fs.ErrExist", err)
	}
	reader, err := OpenFile(path, os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	_ = reader.Close()
}

func TestDirOpenDirChildAndMkdir(t *testing.T) {
	dir, path := openTempDir(t)
	if err := dir.Mkdir("child", Inherited); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := dir.Mkdir("child", Inherited); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Mkdir = %v, want fs.ErrExist", err)
	}
	child, err := dir.OpenDir("child")
	if err != nil {
		t.Fatalf("OpenDir(child): %v", err)
	}
	defer child.Close()
	if child.Name() != filepath.Join(path, "child") {
		t.Fatalf("child name = %q", child.Name())
	}
	writeFile(t, child, "inner", "x")
	entries, err := child.File().ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != "inner" {
		t.Fatalf("ReadDir through the held handle = %v, %v", entries, err)
	}
	info, err := child.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("Stat(child) = %+v, %v", info, err)
	}
}

// G2: a held directory keeps addressing the directory it opened after its
// path is renamed and replaced.
func TestDirStaysBoundAfterPathReplaced(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "work")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(original)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.Rename(original, filepath.Join(root, "moved")); err != nil {
		t.Fatalf("rename held directory: %v", err)
	}
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "entry", "bound")
	if got := readFile(t, filepath.Join(root, "moved", "entry")); got != "bound" {
		t.Fatalf("entry landed elsewhere: %q", got)
	}
	if _, err := os.Stat(filepath.Join(original, "entry")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("new directory at the old path must stay empty: %v", err)
	}
}

func TestClosedDir(t *testing.T) {
	path := t.TempDir()
	dir, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.OpenFile("x", os.O_RDWR|os.O_CREATE, Inherited); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("OpenFile on a closed Dir = %v, want os.ErrClosed", err)
	}
	if _, err := dir.Sync(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Sync on a closed Dir = %v, want os.ErrClosed", err)
	}
	for op, err := range map[string]error{
		"Remove":   dir.Remove("x"),
		"Mkdir":    dir.Mkdir("x", Inherited),
		"Rename":   dir.Rename("x", dir, "y"),
		"OpenDir":  second(dir.OpenDir("x")),
		"Lstat":    second(dir.Lstat("x")),
		"Stat":     second(dir.Stat()),
		"LinkIf":   second(dir.LinkIfAbsent("x", dir, "y")),
		"Identity": second(Identity(dir.File())),
	} {
		if !errors.Is(err, os.ErrClosed) {
			t.Errorf("%s on a closed Dir = %v, want os.ErrClosed", op, err)
		}
	}
}

func second[T any](_ T, err error) error {
	return err
}

// Close racing with operations: every operation either completes in the held
// directory or fails with os.ErrClosed. None may act through a handle value
// that Close freed and another open reused; decoy goroutines keep reopening a
// directory so such a reuse would land in it.
func TestDirCloseDuringOperations(t *testing.T) {
	root := t.TempDir()
	decoy := filepath.Join(root, "decoy")
	if err := os.Mkdir(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var decoys sync.WaitGroup
	for range 2 {
		decoys.Add(1)
		go func() {
			defer decoys.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if held, err := OpenDir(decoy); err == nil {
					_ = held.Close()
				}
			}
		}()
	}
	var (
		mutex      sync.Mutex
		unexpected []error
		closed     int
	)
	for round := range 40 {
		target := filepath.Join(root, fmt.Sprintf("target-%d", round))
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		dir, err := OpenDir(target)
		if err != nil {
			t.Fatal(err)
		}
		var workers sync.WaitGroup
		for worker := range 4 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for step := range 24 {
					err := raceOperation(dir, fmt.Sprintf("w%d-%d", worker, step), step)
					mutex.Lock()
					switch {
					case errors.Is(err, os.ErrClosed):
						closed++
					case err != nil:
						unexpected = append(unexpected, err)
					}
					mutex.Unlock()
				}
			}()
		}
		time.Sleep(time.Duration(round%5) * 200 * time.Microsecond)
		if err := dir.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		workers.Wait()
	}
	close(stop)
	decoys.Wait()
	for _, err := range unexpected {
		t.Errorf("operation racing Close: %v", err)
	}
	entries, err := os.ReadDir(decoy)
	if err != nil || len(entries) != 0 {
		t.Fatalf("operations reached the decoy directory: %v, %v", entries, err)
	}
	t.Logf("%d operations failed with os.ErrClosed after Close", closed)
}

func raceOperation(dir *Dir, name string, step int) error {
	switch step % 4 {
	case 0:
		file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited)
		if err != nil {
			return err
		}
		return file.Close()
	case 1:
		return dir.Mkdir(name, Inherited)
	case 2:
		_, err := dir.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	_, err := dir.Sync()
	return err
}

// setUnownedReparseTag gives path a reparse point whose tag is not a name
// surrogate and is owned by no installed filter.
func setUnownedReparseTag(t *testing.T, path string) {
	t.Helper()
	const unownedTag = 0x80000099
	handle, err := createFile(path, windows.GENERIC_WRITE, 0, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandle(handle)
	buffer := make([]byte, 8+4)
	binary.LittleEndian.PutUint32(buffer[0:], unownedTag)
	binary.LittleEndian.PutUint16(buffer[4:], 4)
	var returned uint32
	err = windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil)
	if err != nil {
		t.Skipf("cannot set a non-surrogate reparse point here: %v", err)
	}
}

// Deduplicated files and cloud placeholders are reparse points that are not
// name surrogates; neither could be created on this machine, so a tag without
// a filter stands in. A data open must ask the filter that owns the tag (here
// none, hence ERROR_CANT_ACCESS_FILE) instead of reading the raw stub, while
// metadata operations still act on the entry itself.
func TestNonSurrogateReparsePointIsOpenedThroughItsFilter(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "stub", "raw stub bytes")
	setUnownedReparseTag(t, filepath.Join(path, "stub"))
	info, err := dir.Lstat("stub")
	if err != nil || info.Attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 || info.IsNameSurrogate() {
		t.Fatalf("Lstat(stub) = %+v, %v; want a non-surrogate reparse point", info, err)
	}
	if _, err := dir.OpenFile("stub", os.O_RDONLY, Inherited); !errors.Is(err, windows.ERROR_CANT_ACCESS_FILE) {
		t.Fatalf("Dir.OpenFile(stub) = %v, want ERROR_CANT_ACCESS_FILE from the filter path", err)
	}
	if _, err := OpenFile(filepath.Join(path, "stub"), os.O_RDONLY, Inherited); !errors.Is(err, windows.ERROR_CANT_ACCESS_FILE) {
		t.Fatalf("OpenFile(stub) = %v, want ERROR_CANT_ACCESS_FILE from the filter path", err)
	}
	if err := dir.Remove("stub"); err != nil {
		t.Fatalf("Remove(stub) must act on the entry itself: %v", err)
	}
}

// The reopen step of finishDataOpen keeps the file and yields a usable handle.
func TestReopenThroughFilterKeepsIdentity(t *testing.T) {
	dir, _ := openTempDir(t)
	writeFile(t, dir, "plain", "content")
	writeFile(t, dir, "other", "x")
	request, err := openRequestFor(os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := ntOpen(mustHandle(t, dir), "plain", request.access, windows.FILE_OPEN, request.options, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandle(handle)
	reopened, err := reopenThroughFilter(handle, request.access, request.options)
	if err != nil {
		t.Fatalf("reopenThroughFilter: %v", err)
	}
	file := os.NewFile(uintptr(reopened), "plain")
	defer file.Close()
	if content, err := io.ReadAll(file); err != nil || string(content) != "content" {
		t.Fatalf("read through the reopened handle = %q, %v", content, err)
	}
	other, err := ntOpen(mustHandle(t, dir), "other", request.access, windows.FILE_OPEN, request.options, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandle(other)
	if err := requireSameFile(handle, other); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("requireSameFile(different files) = %v, want ELOOP", err)
	}
}
