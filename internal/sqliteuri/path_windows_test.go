package sqliteuri

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// The driver opens, writes and re-reads a database named by a drive-path URI,
// read-write and read-only, so the URI form above is the one SQLite accepts.
func TestPathOpensADriveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub dir", "x.db")
	if filepath.VolumeName(path) == "" {
		t.Fatalf("temporary path %q has no volume name", path)
	}
	open := func(mode string) *sql.DB {
		t.Helper()
		uri := url.URL{Scheme: "file", Path: Path(path), RawQuery: url.Values{"mode": {mode}}.Encode()}
		database, err := sql.Open("sqlite", uri.String())
		if err != nil {
			t.Fatalf("open %s: %v", uri.String(), err)
		}
		t.Cleanup(func() { _ = database.Close() })
		return database
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writer := open("rwc")
	if _, err := writer.Exec("CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('ok')"); err != nil {
		t.Fatalf("write through %q: %v", Path(path), err)
	}
	var value string
	if err := open("ro").QueryRow("SELECT v FROM t").Scan(&value); err != nil || value != "ok" {
		t.Fatalf("read-only read = %q, %v; want ok", value, err)
	}
}
