package sqliteuri

import (
	"net/url"
	"path/filepath"
	"testing"
)

// Every path without a volume name, which is every Linux and macOS path, is
// returned unchanged, so DSNs on those systems are byte-identical to the ones
// built from the path itself.
func TestPathIsIdentityWithoutAVolume(t *testing.T) {
	for _, path := range []string{
		"",
		"/",
		"/tmp/haft/.haft/projects/qnt_a7f3b2c1/haft.db",
		"/a dir/with?query#and%percent/x.db",
		"relative/x.db",
		`/odd\name.db`,
	} {
		if got := pathForVolume(path, ""); got != path {
			t.Fatalf("pathForVolume(%q, \"\") = %q, want the path unchanged", path, got)
		}
	}
	path := filepath.Join(t.TempDir(), "x.db")
	if filepath.VolumeName(path) == "" && Path(path) != path {
		t.Fatalf("Path(%q) = %q, want the path unchanged", path, Path(path))
	}
}

func TestPathMovesADriveOutOfTheURIAuthority(t *testing.T) {
	got := pathForVolume(`D:\work\.haft\projects\p\haft.db`, "D:")
	if want := "/D:/work/.haft/projects/p/haft.db"; got != want {
		t.Fatalf("pathForVolume = %q, want %q", got, want)
	}
	uri := url.URL{Scheme: "file", Path: got, RawQuery: "mode=ro"}
	if want := "file:///D:/work/.haft/projects/p/haft.db?mode=ro"; uri.String() != want {
		t.Fatalf("URI = %q, want %q", uri.String(), want)
	}
	if parsed, err := url.Parse(uri.String()); err != nil || parsed.Host != "" || parsed.Path != got {
		t.Fatalf("parsed URI = %+v, %v; want an empty authority and path %q", parsed, err, got)
	}
}

func TestPathKeepsAUNCServerInThePath(t *testing.T) {
	got := pathForVolume(`\\server\share\haft\x.db`, `\\server\share`)
	if want := "//server/share/haft/x.db"; got != want {
		t.Fatalf("pathForVolume = %q, want %q", got, want)
	}
	uri := url.URL{Scheme: "file", Path: got}
	if want := "file:////server/share/haft/x.db"; uri.String() != want {
		t.Fatalf("URI = %q, want %q", uri.String(), want)
	}
}
