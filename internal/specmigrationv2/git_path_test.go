package specmigrationv2

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHostPathFromGitIsIdentityWithASlashSeparator(t *testing.T) {
	inputs := []string{
		"/work/repo",
		"/",
		"C:/work/repo",
		"work/repo/",
		`/work/back\slash`,
		"",
	}
	for _, input := range inputs {
		want := input
		if filepath.Separator != '/' {
			want = strings.ReplaceAll(input, "/", string(filepath.Separator))
		}
		if got := hostPathFromGit(input); got != want {
			t.Fatalf("hostPathFromGit(%q) = %q, want %q", input, got, want)
		}
	}
}
