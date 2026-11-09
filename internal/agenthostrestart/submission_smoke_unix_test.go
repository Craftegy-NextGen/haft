//go:build darwin || linux

package agenthostrestart

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmokeCommandPassesMetacharactersAsOneArgument(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	script := filepath.Join(root, "echo-argv")
	content := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
	if err := os.WriteFile(script, content, 0o700); err != nil {
		t.Fatalf("write argv script: %v", err)
	}
	argument := "$(touch " + marker + ")"
	output := bytes.NewBuffer(nil)
	if err := runSmokeCommand(context.Background(), root, "argv", script, []string{argument}, output); err != nil {
		t.Fatalf("runSmokeCommand: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metacharacter argument was evaluated: %v", err)
	}
	if !strings.Contains(output.String(), argument) {
		t.Fatalf("argument not preserved in output: %s", output.String())
	}
}
