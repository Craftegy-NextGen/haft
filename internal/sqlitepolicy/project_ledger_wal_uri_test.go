package sqlitepolicy

import "testing"

// DSNs built with sqliteuri.Path carry "/D:/..." or "//server/share/..." in the
// URI path on Windows; the project-ledger classification must still find the
// .haft/projects/<id>/haft.db shape, or the WAL policy would silently not apply.
func TestIsWritableProjectLedgerDSNAcceptsVolumePaths(t *testing.T) {
	for _, dsn := range []string{
		"file:///D:/work/.haft/projects/qnt_a7f3b2c1/haft.db?_pragma=busy_timeout%285000%29",
		"file:////server/share/work/.haft/projects/qnt_a7f3b2c1/haft.db",
	} {
		if !isWritableProjectLedgerDSN(dsn) {
			t.Fatalf("isWritableProjectLedgerDSN(%q) = false, want a writable project ledger", dsn)
		}
	}
	if dsn := "file:///D:/work/.haft/projects/qnt_a7f3b2c1/haft.db?mode=ro"; isWritableProjectLedgerDSN(dsn) {
		t.Fatalf("isWritableProjectLedgerDSN(%q) = true, want read-only excluded", dsn)
	}
}
