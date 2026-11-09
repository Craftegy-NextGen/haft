package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const processTreeHelperEnv = "RACE_SHARD_PROCESS_TREE_HELPER"

// TestRaceShardProcessTreeHelper is not a test: the Windows process-tree test
// re-executes the test binary as a child that starts a grandchild.
func TestRaceShardProcessTreeHelper(t *testing.T) {
	if os.Getenv(processTreeHelperEnv) != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index < 0 || index+3 >= len(os.Args) {
		os.Exit(2)
	}
	readyPath, survivedPath := os.Args[index+2], os.Args[index+3]
	switch os.Args[index+1] {
	case "child":
		grandchild := exec.Command(os.Args[0], "-test.run=^TestRaceShardProcessTreeHelper$", "--", "grandchild", readyPath, survivedPath)
		if err := grandchild.Run(); err != nil {
			os.Exit(4)
		}
	case "grandchild":
		// Like the Unix fixture's `trap '' TERM`: a graceful request would not
		// stop this process.
		signal.Ignore(os.Interrupt)
		if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
			os.Exit(5)
		}
		time.Sleep(500 * time.Millisecond)
		_ = os.WriteFile(survivedPath, []byte("survived"), 0o600)
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

// Windows counterpart of TestLocalProcessCancellationTerminatesSignalIgnoringDescendant
// (process_group_unix_test.go): cancellation ends the command's grandchild too.
func TestLocalProcessCancellationTerminatesGrandchildInJob(t *testing.T) {
	root, err := findModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	survivedPath := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan processResult, 1)
	go func() {
		result <- (localProcessExecutor{
			directory:   root,
			environment: []string{processTreeHelperEnv + "=1"},
		}).Run(
			ctx,
			io.Discard,
			io.Discard,
			os.Args[0],
			"-test.run=^TestRaceShardProcessTreeHelper$",
			"--",
			"child",
			readyPath,
			survivedPath,
		)
	}()

	readyDeadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("grandchild did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case observed := <-result:
		if !errors.Is(observed.err, context.Canceled) {
			t.Fatalf("canceled process error = %v", observed.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled process tree did not terminate")
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(survivedPath); !os.IsNotExist(err) {
		t.Fatalf("grandchild survived cancellation: %v", err)
	}
}
