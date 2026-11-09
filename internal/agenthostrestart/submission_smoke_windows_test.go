//go:build windows

package agenthostrestart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows counterpart of TestSmokeCommandPassesMetacharactersAsOneArgument in
// submission_smoke_unix_test.go (ADR-093 E6). The Unix test runs a /bin/sh
// script; here the test binary re-executes itself as the fixture executable
// and prints the arguments it received, one per line.

const smokeHelperEnvironment = "HAFT_AGENTHOSTRESTART_SMOKE_HELPER"

func TestSmokeHelperProcess(t *testing.T) {
	if os.Getenv(smokeHelperEnvironment) != "1" {
		return
	}
	_, arguments, found := cutArguments(os.Args, "--")
	if !found {
		os.Exit(2)
	}
	for _, argument := range arguments {
		fmt.Println(argument)
	}
	os.Exit(0)
}

func cutArguments(arguments []string, separator string) ([]string, []string, bool) {
	for index, argument := range arguments {
		if argument == separator {
			return arguments[:index], arguments[index+1:], true
		}
	}
	return arguments, nil, false
}

func TestSmokeCommandPassesMetacharactersAsOneArgument(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	t.Setenv(smokeHelperEnvironment, "1")
	argument := "$(touch " + marker + ")"
	output := bytes.NewBuffer(nil)
	arguments := []string{"-test.run=^TestSmokeHelperProcess$", "--", argument}
	if err := runSmokeCommand(context.Background(), root, "argv", executable, arguments, output); err != nil {
		t.Fatalf("runSmokeCommand: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metacharacter argument was evaluated: %v", err)
	}
	if !strings.Contains(output.String(), argument) {
		t.Fatalf("argument not preserved in output: %s", output.String())
	}
}
