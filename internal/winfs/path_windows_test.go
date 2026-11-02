package winfs

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	accepted := []string{
		"file", "a.b", ".hidden", "..x", "name with space", "ünïcode",
		"CONSOLE", "NULL", "COM10", "LPT", "COMX", "AUXILIARY.txt", "xCON", "conın$",
	}
	for _, name := range accepted {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	rejected := []string{
		"", ".", "..", `a\b`, "a/b", "a:b", "file:stream", `\`, "/", "a\x00b",
		"...", "trailing.", "trailing ", "x. ",
		"NUL", "nul", "CON", "Con", "PRN", "AUX", "NUL.txt", "con.tar.gz", "NUL .txt", "aux  ",
		"COM0", "COM1", "com9", "LPT0", "LPT1", "lpt9", "COM1.log",
		"COM¹", "LPT²", "com³.txt",
		"CONIN$", "conout$", "CONIN$.x",
	}
	for _, name := range rejected {
		err := ValidateName(name)
		if !errors.Is(err, ErrInvalidName) || !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName and fs.ErrInvalid", name, err)
		}
	}
}

// A reserved name never reaches the NT API, where it would create a real
// entry that Win32 paths resolve to a device instead.
func TestDirRejectsReservedNames(t *testing.T) {
	dir, path := openTempDir(t)
	for _, name := range []string{"NUL", "con.txt", "trailing."} {
		if _, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited); !errors.Is(err, ErrInvalidName) {
			t.Errorf("OpenFile(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reserved names must not create entries: %v, %v", entries, err)
	}
}

func TestExtendedPath(t *testing.T) {
	long := `D:\` + strings.Repeat(`segment\`, 40) + "file"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"short path unchanged", `D:\short\file`, `D:\short\file`},
		{"relative path unchanged", strings.Repeat("r", 300), strings.Repeat("r", 300)},
		{"long drive path", long, `\\?\` + long},
		{"long path is cleaned", long + `\.\x\..`, `\\?\` + long},
		{"long UNC path", `\\server\share\` + strings.Repeat("u", 300), `\\?\UNC\server\share\` + strings.Repeat("u", 300)},
		{"already extended", `\\?\` + long, `\\?\` + long},
	}
	for _, test := range tests {
		if got := extendedPath(test.in); got != test.want {
			t.Errorf("%s: extendedPath = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestOpenRequestForRejectsUnsupportedFlags(t *testing.T) {
	_, err := openRequestFor(0x4000000)
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("openRequestFor(unsupported) = %v, want fs.ErrInvalid", err)
	}
	_, err = openRequestFor(0x3)
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("openRequestFor(O_WRONLY|O_RDWR) = %v, want fs.ErrInvalid", err)
	}
}
