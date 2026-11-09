package code

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSlashPathIsIdentityWithASlashSeparator(t *testing.T) {
	inputs := []string{
		"answer.go",
		"other/other_test.go",
		"a/b/c.go",
		"/abs/x.go",
		`odd\name.go`,
		"",
	}
	for _, input := range inputs {
		want := input
		if filepath.Separator != '/' {
			want = strings.ReplaceAll(input, string(filepath.Separator), "/")
		}
		if got := slashPath(input); got != want {
			t.Fatalf("slashPath(%q) = %q, want %q", input, got, want)
		}
	}
}
