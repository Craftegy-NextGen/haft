//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalProcessCancellationTerminatesSignalIgnoringDescendant(
	t *testing.T,
) {
	root, err := findModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	survivedPath := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan processResult, 1)
	go func() {
		result <- (localProcessExecutor{directory: root}).Run(
			ctx,
			io.Discard,
			io.Discard,
			"/bin/sh",
			"-c",
			`(trap '' TERM; printf ready > "$1"; sleep 0.5; printf survived > "$2") & wait`,
			"race-shard-process-tree",
			readyPath,
			survivedPath,
		)
	}()

	readyDeadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("signal-ignoring descendant did not become ready")
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
		t.Fatal("canceled process group did not terminate")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(survivedPath); !os.IsNotExist(err) {
		t.Fatalf(
			"signal-ignoring descendant survived cancellation: %v",
			err,
		)
	}
}
