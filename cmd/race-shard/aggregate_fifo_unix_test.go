//go:build !windows

package main

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/racequalification"
)

func TestAggregateModeRejectsNonRegularInputWithoutBlocking(t *testing.T) {
	t.Parallel()

	input := filepath.Join(t.TempDir(), "shard.fifo")
	if err := syscall.Mkfifo(input, 0o600); err != nil {
		t.Fatalf("create aggregate input FIFO: %v", err)
	}
	output := filepath.Join(t.TempDir(), "aggregate.json")
	result := make(chan struct {
		exitCode int
		stderr   string
	}, 1)
	go func() {
		var errorOutput strings.Builder
		exitCode := runCLI(
			context.Background(),
			aggregateArgs([]string{input}, output),
			testApplication(&fakeExecutor{}),
			&errorOutput,
		)
		result <- struct {
			exitCode int
			stderr   string
		}{
			exitCode: exitCode,
			stderr:   errorOutput.String(),
		}
	}()

	select {
	case got := <-result:
		if got.exitCode != 1 {
			t.Fatalf("exit = %d, want 1; stderr = %s", got.exitCode, got.stderr)
		}
		document := readRunDocument(t, output)
		if document.Status != string(racequalification.AggregateInvalid) ||
			!strings.Contains(document.Error, "regular file") {
			t.Fatalf("non-regular input document = %#v", document)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aggregate mode blocked while opening a non-regular input")
	}
}
