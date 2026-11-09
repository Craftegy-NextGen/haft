//go:build windows

package p14acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// Windows half of the ADR-093 E3 split of codex_session_history_test.go (see
// codex_session_identity_unix_test.go). The Unix function reads st_dev and
// st_ino from the FileInfo; os.FileInfo on Windows carries no file identity
// in Sys(), but os.SameFile fetches the volume serial number and the file
// index from the file handle and keeps them in the unexported vol, idxhi and
// idxlo fields of the standard library's file stat. The function forces that
// load and reads the three values by reflection, so the callers keep the
// os.FileInfo signature. It fails closed when the standard library layout
// changes (the field names and types were checked against Go 1.25.8, the
// version go.mod pins, in os/types_windows.go). Device is the volume serial
// number and Inode is the 64-bit file index, the low half of the FILE_ID_INFO
// identity that winfs reports (the test below pins that relation).

func p14CodexSessionIdentityFromFileInfo(
	info os.FileInfo,
) (p14CodexSessionFileIdentity, error) {
	if info == nil || !os.SameFile(info, info) {
		return p14CodexSessionFileIdentity{}, fmt.Errorf(
			"P14 Codex session history has no stable volume/file-index identity",
		)
	}
	value := reflect.ValueOf(info)
	if value.Kind() != reflect.Pointer || value.IsNil() ||
		value.Elem().Kind() != reflect.Struct {
		return p14CodexSessionFileIdentity{}, fmt.Errorf(
			"P14 Codex session history has no stable volume/file-index identity",
		)
	}
	stat := value.Elem()
	volume := stat.FieldByName("vol")
	high := stat.FieldByName("idxhi")
	low := stat.FieldByName("idxlo")
	if !volume.IsValid() || !high.IsValid() || !low.IsValid() ||
		volume.Kind() != reflect.Uint32 ||
		high.Kind() != reflect.Uint32 ||
		low.Kind() != reflect.Uint32 {
		return p14CodexSessionFileIdentity{}, fmt.Errorf(
			"P14 Codex session history has no stable volume/file-index identity",
		)
	}
	device := volume.Uint()
	index := high.Uint()<<32 | low.Uint()
	if device == 0 || index == 0 {
		return p14CodexSessionFileIdentity{}, fmt.Errorf(
			"P14 Codex session history has no stable volume/file-index identity",
		)
	}
	return p14CodexSessionFileIdentity{
		Device: device,
		Inode:  index,
	}, nil
}

func TestP14CodexSessionIdentityMatchesFileIdentityOnWindows(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.jsonl")
	second := filepath.Join(dir, "second.jsonl")
	link := filepath.Join(dir, "link.jsonl")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(first, link); err != nil {
		t.Fatal(err)
	}

	identityOf := func(path string) p14CodexSessionFileIdentity {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := p14CodexSessionIdentityFromFileInfo(info)
		if err != nil {
			t.Fatal(err)
		}
		return identity
	}
	firstIdentity := identityOf(first)
	if firstIdentity.Device == 0 || firstIdentity.Inode == 0 {
		t.Fatalf("identity %+v has a zero field", firstIdentity)
	}
	if got := identityOf(first); got != firstIdentity {
		t.Fatalf("identity changed between two Lstat calls: %+v != %+v", got, firstIdentity)
	}
	if got := identityOf(link); got != firstIdentity {
		t.Fatalf("hard link identity %+v != %+v", got, firstIdentity)
	}
	if got := identityOf(second); got == firstIdentity {
		t.Fatalf("two distinct files share the identity %+v", got)
	}

	file, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := p14CodexSessionIdentityFromFileInfo(openedInfo)
	if err != nil {
		t.Fatal(err)
	}
	if opened != firstIdentity {
		t.Fatalf("open handle identity %+v != Lstat identity %+v", opened, firstIdentity)
	}

	// The Lstat and handle identities are the low half of winfs FILE_ID_INFO.
	fileID, err := winfs.Identity(file)
	if err != nil {
		t.Fatal(err)
	}
	if fileID.VolumeSerial&0xffffffff != firstIdentity.Device {
		t.Fatalf(
			"volume serial %016x does not end in device %08x",
			fileID.VolumeSerial,
			firstIdentity.Device,
		)
	}
	var low uint64
	for i := 7; i >= 0; i-- {
		low = low<<8 | uint64(fileID.ID[i])
	}
	if low != firstIdentity.Inode {
		t.Fatalf("FILE_ID_INFO low half %016x != inode %016x", low, firstIdentity.Inode)
	}
}

func TestP14CodexSessionIdentityRejectsNilFileInfoOnWindows(t *testing.T) {
	if _, err := p14CodexSessionIdentityFromFileInfo(nil); err == nil {
		t.Fatal("a nil FileInfo must have no identity")
	}
}
