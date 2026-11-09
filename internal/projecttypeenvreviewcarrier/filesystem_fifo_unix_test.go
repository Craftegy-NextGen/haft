//go:build darwin || linux

package projecttypeenvreviewcarrier

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadRejectsFIFOWithoutBlocking(t *testing.T) {
	const childMode = "HAFT_GENESIS_REVIEW_FIFO_CHILD"
	const rootVariable = "HAFT_GENESIS_REVIEW_FIFO_ROOT"
	if os.Getenv(childMode) == "1" {
		root := os.Getenv(rootVariable)
		if _, err := Read(root); err == nil {
			t.Fatal("read accepted FIFO carrier")
		}
		return
	}
	root := newProjectRoot(t)
	target := filepath.Join(root, DirectoryName, FileName)
	if err := unix.Mkfifo(target, 0o600); err != nil {
		t.Fatalf("create carrier FIFO: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestReadRejectsFIFOWithoutBlocking$",
	)
	command.Env = append(
		os.Environ(),
		childMode+"=1",
		rootVariable+"="+root,
	)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("FIFO read blocked until timeout: %v; output=%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("FIFO rejection subprocess failed: %v; output=%s", err, output)
	}
}

func TestReadRejectsFIFOLockWithoutBlocking(t *testing.T) {
	const childMode = "HAFT_GENESIS_REVIEW_FIFO_LOCK_CHILD"
	const rootVariable = "HAFT_GENESIS_REVIEW_FIFO_LOCK_ROOT"
	if os.Getenv(childMode) == "1" {
		root := os.Getenv(rootVariable)
		if _, err := Read(root); err == nil {
			t.Fatal("read accepted FIFO lock")
		}
		return
	}
	root := newProjectRoot(t)
	carrier := mustCarrier(t, []byte("{\"review\":\"current\"}\n"))
	target := filepath.Join(root, DirectoryName, FileName)
	if err := os.WriteFile(target, carrier.Bytes(), 0o600); err != nil {
		t.Fatalf("write carrier: %v", err)
	}
	lockPath := filepath.Join(root, DirectoryName, lockFileName)
	if err := unix.Mkfifo(lockPath, 0o600); err != nil {
		t.Fatalf("create lock FIFO: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestReadRejectsFIFOLockWithoutBlocking$",
	)
	command.Env = append(
		os.Environ(),
		childMode+"=1",
		rootVariable+"="+root,
	)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf(
			"FIFO lock read blocked until timeout: %v; output=%s",
			ctx.Err(),
			output,
		)
	}
	if err != nil {
		t.Fatalf(
			"FIFO lock rejection subprocess failed: %v; output=%s",
			err,
			output,
		)
	}
}
