package testpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Where the separator is "/" every path is returned unchanged, so fixtures on
// Linux and macOS are byte-identical to the literals they wrap.
func TestAbsIsIdentityWithASlashSeparator(t *testing.T) {
	for _, path := range []string{"", "/", "/tmp/haft-project", "/a b/c?d#e", "relative/x", `/odd\name`} {
		if got := absFor(path, '/', ""); got != path {
			t.Fatalf("absFor(%q, '/', \"\") = %q, want the path unchanged", path, got)
		}
		if got := absFor(path, '/', "D:"); got != path {
			t.Fatalf("absFor(%q, '/', \"D:\") = %q, want the path unchanged", path, got)
		}
	}
	if filepath.Separator == '/' && Abs("/tmp/haft-project") != "/tmp/haft-project" {
		t.Fatalf("Abs changed a fixture on a slash-separated OS: %q", Abs("/tmp/haft-project"))
	}
}

func TestAbsMakesAWindowsAbsolutePath(t *testing.T) {
	if got, want := absFor("/tmp/haft-project", '\\', "D:"), `D:\tmp\haft-project`; got != want {
		t.Fatalf("absFor = %q, want %q", got, want)
	}
	if !filepath.IsAbs(Abs("/tmp/haft-project")) {
		t.Fatalf("Abs(%q) = %q is not absolute on this OS", "/tmp/haft-project", Abs("/tmp/haft-project"))
	}
	if path := Abs("/tmp/haft-project"); filepath.Clean(path) != path {
		t.Fatalf("Abs(%q) = %q is not clean on this OS", "/tmp/haft-project", path)
	}
}

func TestSetHomeSetsBothHomeVariables(t *testing.T) {
	dir := t.TempDir()
	SetHome(t, dir)
	if os.Getenv("HOME") != dir || os.Getenv("USERPROFILE") != dir {
		t.Fatalf("HOME = %q, USERPROFILE = %q, want both %q", os.Getenv("HOME"), os.Getenv("USERPROFILE"), dir)
	}
	if home, err := os.UserHomeDir(); err != nil || home != dir {
		t.Fatalf("os.UserHomeDir() = %q, %v; want %q", home, err, dir)
	}
}

func TestJSONStringEscapesBackslashes(t *testing.T) {
	if got, want := JSONString(`D:\tmp\x`), `"D:\\tmp\\x"`; got != want {
		t.Fatalf("JSONString = %s, want %s", got, want)
	}
	if got, want := JSONString("/tmp/x"), `"/tmp/x"`; got != want {
		t.Fatalf("JSONString = %s, want %s", got, want)
	}
}

// Where the separator is "/" every path is returned unchanged, so fixture
// binary paths on Linux and macOS are byte-identical to the paths they wrap.
func TestExeIsIdentityWithASlashSeparator(t *testing.T) {
	for _, path := range []string{"", "/", "/tmp/bin/haft10", "relative/new-haft10", "/a b/c.d", `/odd\name`} {
		if got := exeFor(path, '/'); got != path {
			t.Fatalf("exeFor(%q, '/') = %q, want the path unchanged", path, got)
		}
	}
	if filepath.Separator == '/' && Exe("/tmp/bin/haft10") != "/tmp/bin/haft10" {
		t.Fatalf("Exe changed a fixture on a slash-separated OS: %q", Exe("/tmp/bin/haft10"))
	}
}

func TestExeAppendsAnExecutableExtensionOnWindows(t *testing.T) {
	if got, want := exeFor(`D:\tmp\bin\haft10`, '\\'), `D:\tmp\bin\haft10.exe`; got != want {
		t.Fatalf("exeFor = %q, want %q", got, want)
	}
	if filepath.Separator == '\\' && Exe(`D:\tmp\new-haft10`) != `D:\tmp\new-haft10.exe` {
		t.Fatalf("Exe on Windows = %q, want the .exe suffix", Exe(`D:\tmp\new-haft10`))
	}
}

// Where the separator is "/" every name is returned unchanged, so fixture
// names on Linux and macOS are byte-identical to the names they wrap.
func TestNameIsIdentityWithASlashSeparator(t *testing.T) {
	for _, name := range []string{"", "answer.go", "Original-\u00e9\"Root", strings.Repeat("\x01", 101) + ".go", "a<b>c:d|e?f*g", `odd\name`} {
		if got := nameFor(name, '/'); got != name {
			t.Fatalf("nameFor(%q, '/') = %q, want the name unchanged", name, got)
		}
	}
	if filepath.Separator == '/' && Name("Original-\u00e9\"Root") != "Original-\u00e9\"Root" {
		t.Fatalf("Name changed a fixture on a slash-separated OS: %q", Name("Original-\u00e9\"Root"))
	}
}

func TestNameMakesAWindowsLegalName(t *testing.T) {
	if got, want := nameFor("Original-\u00e9\"Root", '\\'), "Original-\u00e9\uff02Root"; got != want {
		t.Fatalf("nameFor quote = %q, want %q", got, want)
	}
	if got, want := nameFor("\x00\x01\x1f.go", '\\'), "\u2028\u2028\u2028.go"; got != want {
		t.Fatalf("nameFor control = %q, want %q", got, want)
	}
	if got, want := nameFor("a<b>c:d|e?f*g", '\\'), "a\uff1cb\uff1ec\uff1ad\uff5ce\uff1ff\uff0ag"; got != want {
		t.Fatalf("nameFor punctuation = %q, want %q", got, want)
	}
	if got := nameFor("plain-name.go", '\\'); got != "plain-name.go" {
		t.Fatalf("nameFor changed a legal name: %q", got)
	}
	if filepath.Separator == '\\' {
		path := filepath.Join(t.TempDir(), Name(strings.Repeat("\x01", 3)+"x\"y.go"))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("Windows rejected the stand-in name: %v", err)
		}
	}
}

// A path without a backslash or a double quote, which is every temporary
// directory path on Linux and macOS, is returned unchanged, so fixtures that
// look for it in a TOML file are byte-identical to the path they wrap.
func TestTOMLEscapeIsIdentityWithoutBackslashOrQuote(t *testing.T) {
	for _, path := range []string{"", "/", "/tmp/bin/haft10", "relative/new-haft10", "/a b/c.d", "/p\u00e9/x"} {
		if got := TOMLEscape(path); got != path {
			t.Fatalf("TOMLEscape(%q) = %q, want the path unchanged", path, got)
		}
	}
}

func TestTOMLEscapeDoublesBackslashesAndEscapesQuotes(t *testing.T) {
	if got, want := TOMLEscape(`D:\tmp\bin\haft10.exe`), `D:\\tmp\\bin\\haft10.exe`; got != want {
		t.Fatalf("TOMLEscape = %q, want %q", got, want)
	}
	if got, want := TOMLEscape(`a"b`), `a\"b`; got != want {
		t.Fatalf("TOMLEscape = %q, want %q", got, want)
	}
}
