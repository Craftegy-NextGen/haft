//go:build darwin || linux || windows

package checkrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunTimeoutKillsDescendantHoldingOutputPipe(t *testing.T) {
	request := testRequest(t, "spawn-child")
	marker := filepath.Join(request.Root, "child-survival-marker")
	trigger := filepath.Join(request.Root, "child-write-trigger")
	request.Command = append(request.Command, marker, trigger)
	request.Timeout = 500 * time.Millisecond
	result, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != "timeout" || !result.PipeDrainUncertain || !strings.Contains(string(result.Stdout), "spawned\n") {
		t.Fatalf("expected a started child and timeout: %+v", result)
	}
	if err := os.WriteFile(trigger, []byte("write"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The child writes only after Run returns and the test releases it.
	time.Sleep(2 * time.Second)
	_, err = os.Stat(marker)
	if !os.IsNotExist(err) {
		t.Fatalf("child survived timeout; marker stat: %v", err)
	}
}

func TestProcessGroupWatcherExitsWhenRunFinishes(t *testing.T) {
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		_ = watchProcessGroup(context.Background(), &exec.Cmd{}, finished, terminateProcessGroup)
		close(watcherDone)
	}()
	close(finished)
	select {
	case <-watcherDone:
	case <-time.After(time.Second):
		t.Fatal("process-group watcher remained after run completion")
	}
}

func TestWatcherCleanupErrorRemainsVisibleBesidePrimaryFailure(t *testing.T) {
	cleanup := &groupCleanup{}
	permissionError := errors.New("group termination denied")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	cancel()
	watchError := watchProcessGroup(ctx, &exec.Cmd{}, finished, func(*exec.Cmd) error {
		cleanup.record(permissionError)
		return permissionError
	})
	if !errors.Is(watchError, permissionError) || !cleanup.uncertain(true) {
		t.Fatalf("watcher termination failure was lost: error=%v uncertain=%t", watchError, cleanup.uncertain(true))
	}
	for _, primary := range []string{"timeout", "canceled", "wait_failed"} {
		if got := cleanupFailure(primary, cleanup.uncertain(true)); got != primary {
			t.Fatalf("cleanup failure masked %s with %s", primary, got)
		}
	}
	if got := cleanupFailure("", cleanup.uncertain(true)); got != "cleanup_failed" {
		t.Fatalf("cleanup failure lacked primary classification: %s", got)
	}
	cleanup.record(nil)
	if cleanup.uncertain(true) || cleanupFailure("timeout", cleanup.uncertain(true)) != "timeout" {
		t.Fatal("confirmed retry did not resolve cleanup uncertainty")
	}
}

func TestRunCleansOwnedDescendantsAfterParentExit(t *testing.T) {
	for _, tc := range []struct {
		name           string
		failure        string
		exit           int
		stdoutComplete bool
		stderrComplete bool
	}{
		{name: "spawn-child-closed", failure: "", exit: 0, stdoutComplete: true, stderrComplete: true},
		{name: "spawn-child", failure: "capture_incomplete", exit: 0},
		{name: "spawn-child-fail", failure: "capture_incomplete", exit: 7},
		{name: "spawn-child-stderr", failure: "capture_incomplete", exit: 0, stdoutComplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := testRequest(t, tc.name)
			marker := filepath.Join(request.Root, "child-survival-marker")
			trigger := filepath.Join(request.Root, "child-write-trigger")
			request.Command = append(request.Command, marker, trigger)
			result, err := Run(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Failure != tc.failure || !strings.Contains(string(result.Stdout), "spawned\n") || result.OutputExceeded {
				t.Fatalf("parent outcome or pipe completeness changed: %+v", result)
			}
			if result.ExitCode == nil || *result.ExitCode != tc.exit {
				t.Fatalf("parent exit status was lost: %+v", result)
			}
			if result.PipeDrainUncertain != (tc.failure == "capture_incomplete") || result.StdoutDrainComplete != tc.stdoutComplete || result.StderrDrainComplete != tc.stderrComplete {
				t.Fatalf("pipe EOF was not classified independently of process status: %+v", result)
			}
			if err := os.WriteFile(trigger, []byte("write"), 0o600); err != nil {
				t.Fatal(err)
			}
			// The child writes only after Run returns and the test releases it.
			time.Sleep(2 * time.Second)
			_, err = os.Stat(marker)
			if !os.IsNotExist(err) {
				t.Fatalf("owned child survived completed capture: %v", err)
			}
		})
	}
}

func TestOutputDrainUncertaintyIncludesUnresolvedCompletion(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stdoutComplete bool
		stderrComplete bool
		contextErr     error
		failure        string
		want           bool
	}{
		{name: "ordinary-complete", stdoutComplete: true, stderrComplete: true},
		{name: "held-stdout", stderrComplete: true, want: true},
		{name: "timeout", stdoutComplete: true, stderrComplete: true, contextErr: context.DeadlineExceeded, want: true},
		{name: "cancel", stdoutComplete: true, stderrComplete: true, contextErr: context.Canceled, want: true},
		{name: "cleanup-failed", stdoutComplete: true, stderrComplete: true, failure: "cleanup_failed", want: true},
		{name: "wait-failed", stdoutComplete: true, stderrComplete: true, failure: "wait_failed", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := outputDrainUncertain(tc.stdoutComplete, tc.stderrComplete, tc.contextErr, tc.failure)
			if got != tc.want {
				t.Fatalf("output drain uncertainty=%t, want %t", got, tc.want)
			}
		})
	}
}
