package winfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPrivateCreatePassesCheck(t *testing.T) {
	dir, path := openTempDir(t)
	file, err := dir.OpenFile("lease", os.O_RDWR|os.O_CREATE|os.O_EXCL, Private)
	if err != nil {
		t.Fatalf("private create: %v", err)
	}
	defer file.Close()
	if err := CheckPrivate(file); err != nil {
		t.Fatalf("CheckPrivate(handle-relative private create): %v", err)
	}
	byPath, err := OpenFile(filepath.Join(path, "receipt"), os.O_WRONLY|os.O_CREATE, Private)
	if err != nil {
		t.Fatalf("private create by path: %v", err)
	}
	defer byPath.Close()
	if err := CheckPrivate(byPath); err != nil {
		t.Fatalf("CheckPrivate(path private create): %v", err)
	}
	reopened, err := dir.OpenFile("lease", os.O_RDWR|os.O_CREATE, Private)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := CheckPrivate(reopened); err != nil {
		t.Fatalf("CheckPrivate(reopened private file): %v", err)
	}
	user, err := currentUser()
	if err != nil {
		t.Fatal(err)
	}
	if owner := fileOwner(t, filepath.Join(path, "lease")); !owner.Equals(user) {
		t.Fatalf("owner %s, want the token user %s", owner, user)
	}
}

func TestInheritedFileIsNotPrivate(t *testing.T) {
	dir, _ := openTempDir(t)
	writeFile(t, dir, "plain", "x")
	file, err := dir.OpenFile("plain", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = CheckPrivate(file)
	if !errors.Is(err, ErrNotPrivate) || errors.Is(err, ErrElevatedOwner) {
		t.Fatalf("CheckPrivate(inherited) = %v, want ErrNotPrivate only", err)
	}
	// Protection applies only when the open creates the file.
	again, err := dir.OpenFile("plain", os.O_RDWR|os.O_CREATE, Private)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err := CheckPrivate(again); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("open-if of an existing file must not apply Private: %v", err)
	}
}

func TestPrivateFileWithSecondLinkIsNotPrivate(t *testing.T) {
	dir, _ := openTempDir(t)
	file, err := dir.OpenFile("lease", os.O_RDWR|os.O_CREATE|os.O_EXCL, Private)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := dir.LinkIfAbsent("lease", dir, "alias"); err != nil {
		t.Fatal(err)
	}
	err = CheckPrivate(file)
	if !errors.Is(err, ErrNotPrivate) || !strings.Contains(err.Error(), "2 links") {
		t.Fatalf("CheckPrivate(two links) = %v, want ErrNotPrivate with the link count", err)
	}
}

func TestPrivateMkdir(t *testing.T) {
	dir, _ := openTempDir(t)
	if err := dir.Mkdir("state", Private); err != nil {
		t.Fatalf("private Mkdir: %v", err)
	}
	child, err := dir.OpenDir("state")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if err := CheckPrivate(child.File()); err != nil {
		t.Fatalf("CheckPrivate(private directory): %v", err)
	}
}

// A real file owned by the Administrators group needs an elevated token to
// create; the pure owner check below runs everywhere.
func TestElevatedOwnerFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elevated")
	user, err := currentUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:BAD:P(A;;FA;;;" + user.String() + ")(A;;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	attributes := &windows.SecurityAttributes{SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	name, _ := windows.UTF16PtrFromString(path)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, shareAll, attributes,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_INVALID_OWNER) {
		t.Skipf("this token cannot assign the Administrators group as owner (not elevated): %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	err = CheckPrivate(file)
	if !errors.Is(err, ErrElevatedOwner) || !errors.Is(err, ErrNotPrivate) || !strings.Contains(err.Error(), "icacls") {
		t.Fatalf("CheckPrivate(Administrators owner) = %v, want ErrElevatedOwner with a repair hint", err)
	}
	t.Logf("elevated-owner message: %v", err)
}

func TestCheckOwner(t *testing.T) {
	user, err := currentUser()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		sddl     string
		private  bool
		elevated bool
	}{
		{"O:" + user.String(), true, false},
		{"O:BA", false, true},
		{"O:WD", false, false},
		{"O:SY", false, false},
	}
	for _, test := range tests {
		descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
		if err != nil {
			t.Fatal(err)
		}
		err = checkOwner(descriptor, user, `D:\carrier`)
		if test.private != (err == nil) || test.elevated != errors.Is(err, ErrElevatedOwner) {
			t.Errorf("checkOwner(%s) = %v", test.sddl, err)
		}
		if err != nil && !errors.Is(err, ErrNotPrivate) {
			t.Errorf("checkOwner(%s) = %v, must wrap ErrNotPrivate", test.sddl, err)
		}
	}
}

func TestCheckOwnerOnlyDACL(t *testing.T) {
	user, err := currentUser()
	if err != nil {
		t.Fatal(err)
	}
	owner := user.String()
	// reason is a fragment of the expected error, so each case proves which
	// branch rejected it.
	tests := []struct {
		name   string
		sddl   string
		reason string
	}{
		{"owner, SYSTEM, Administrators", "D:P(A;;FA;;;" + owner + ")(A;;FA;;;SY)(A;;FA;;;BA)", ""},
		{"owner only", "D:P(A;;FR;;;" + owner + ")", ""},
		{"empty protected DACL", "D:P", ""},
		{"deny entry for Everyone", "D:P(A;;FA;;;" + owner + ")(D;;FA;;;WD)", ""},
		{"not protected", "D:(A;;FA;;;" + owner + ")", "not protected"},
		{"no DACL and not protected", "O:" + owner, "not protected"},
		{"Everyone allowed", "D:P(A;;FA;;;" + owner + ")(A;;FR;;;WD)", "allows access to S-1-1-0"},
		{"Authenticated Users allowed", "D:P(A;;FA;;;" + owner + ")(A;;0x1301bf;;;AU)", "allows access to S-1-5-11"},
		{"Users allowed", "D:P(A;;FA;;;" + owner + ")(A;;FR;;;BU)", "allows access to S-1-5-32-545"},
		{"NULL DACL", "D:PNO_ACCESS_CONTROL", "NULL DACL"},
		{"object ACE", "D:P(OA;;FA;bf967aba-0de6-11d0-a285-00aa003049e2;;" + owner + ")", "unsupported type 5"},
		{"callback ACE", "D:P(XA;;FA;;;" + owner + ";(Member_of {SID(BA)}))", "unsupported type 9"},
	}
	for _, test := range tests {
		descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
		if err != nil {
			t.Fatalf("%s: parse %s: %v", test.name, test.sddl, err)
		}
		expectDACLResult(t, test.name, checkOwnerOnlyDACL(descriptor, user), test.reason)
	}
	// A protected descriptor without any DACL cannot be written in SDDL.
	absent, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if err := absent.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		t.Fatal(err)
	}
	expectDACLResult(t, "protected without DACL", checkOwnerOnlyDACL(absent, user), "read DACL")
}

func expectDACLResult(t *testing.T, name string, err error, reason string) {
	t.Helper()
	switch {
	case reason == "" && err != nil:
		t.Errorf("%s: checkOwnerOnlyDACL = %v, want private", name, err)
	case reason != "" && (err == nil || !strings.Contains(err.Error(), reason)):
		t.Errorf("%s: checkOwnerOnlyDACL = %v, want an error containing %q", name, err, reason)
	}
}

func TestCheckPrivateFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease")
	file, err := OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, Private)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	err = CheckPrivate(file)
	if !errors.Is(err, ErrNotPrivate) || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("CheckPrivate(closed file) = %v, want ErrNotPrivate and os.ErrClosed", err)
	}
	if err := CheckPrivate(nil); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("CheckPrivate(nil) = %v, want ErrNotPrivate", err)
	}
}

func TestUnknownProtection(t *testing.T) {
	dir, _ := openTempDir(t)
	_, err := dir.OpenFile("x", os.O_RDWR|os.O_CREATE, Protection(7))
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("unknown protection = %v, want fs.ErrInvalid", err)
	}
}
