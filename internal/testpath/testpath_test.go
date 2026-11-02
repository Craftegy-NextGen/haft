package testpath

import (
	"os"
	"path/filepath"
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
