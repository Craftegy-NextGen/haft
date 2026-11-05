package profileprojection

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// TestWindowsTwinMirrorPinMatchesUpstream checks the ADR-091 R-c mirror pin of
// filesystem_windows.go on every OS. It has no build constraint on purpose: when
// an upstream change reaches filesystem_supported.go, Linux and macOS CI fail
// too, until the Windows twin is re-mirrored and re-pinned (the re-pin commit
// shows `git diff <pinned blob> <new blob>` of the mirrored file).
func TestWindowsTwinMirrorPinMatchesUpstream(t *testing.T) {
	checkMirrorPin(
		t,
		"filesystem_windows.go",
		"internal/profileprojection/filesystem_supported.go",
	)
}

func checkMirrorPin(t *testing.T, twin string, mirrored string) {
	t.Helper()
	const prefix = "// winport: mirrors "
	text, err := os.ReadFile(twin)
	if err != nil {
		t.Fatalf("read Windows twin: %v", err)
	}
	var fields []string
	for _, line := range strings.Split(string(text), "\n") {
		if rest, found := strings.CutPrefix(line, prefix); found {
			fields = strings.Fields(rest)
			break
		}
	}
	if len(fields) != 4 || fields[2] != "(blob" || !strings.HasSuffix(fields[3], ")") {
		t.Fatalf("%s has no well-formed %q pin line: %q", twin, prefix, fields)
	}
	if fields[0] != mirrored {
		t.Fatalf("%s mirrors %s, want %s", twin, fields[0], mirrored)
	}
	upstream, err := os.ReadFile(mirrored[strings.LastIndex(mirrored, "/")+1:])
	if err != nil {
		t.Fatalf("read mirrored file: %v", err)
	}
	digest := sha256.Sum256(upstream)
	if got := "sha256:" + hex.EncodeToString(digest[:]); got != fields[1] {
		t.Fatalf(
			"%s changed (%s, pinned %s in %s): re-mirror the Windows twin, then re-pin it",
			mirrored,
			got,
			fields[1],
			twin,
		)
	}
}
