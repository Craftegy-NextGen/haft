// Package checkrunner executes one bounded, explicitly selected local process.
// It is an effect boundary: check contracts and result interpretation live in
// the layers above it. It is not a sandbox for untrusted project code.
package checkrunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var environmentKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type Request struct {
	Root           string
	Command        []string
	Environment    map[string]string
	Timeout        time.Duration
	MaxOutputBytes int
}

// Result carries prefixes of stdout and stderr, and hashes and byte counts of
// all bytes actually drained before any forced pipe close. Environment contains
// only pinned Go variables;
// host PATH, HOME and temporary-directory paths are not serialized. A caller
// must treat OutputExceeded, PipeDrainUncertain, CleanupUncertain or any
// nonempty Failure as an incomplete run.
type Result struct {
	Started        bool
	ExitCode       *int
	Failure        string
	Stdout         []byte
	Stderr         []byte
	StdoutDigest   string
	StderrDigest   string
	StdoutBytes    int64
	StderrBytes    int64
	OutputExceeded bool
	// DrainComplete is per stream. It requires EOF from the owned pipe reader
	// before that reader is forcibly closed; byte counts include everything
	// actually observed before that point.
	StdoutDrainComplete bool
	StderrDrainComplete bool
	// PipeDrainUncertain means observed byte counts and digests may exclude
	// bytes from a pipe closed early. Timeout, cancellation and cleanup errors
	// also make completeness uncertain; this is not proof bytes were omitted.
	PipeDrainUncertain bool
	// CleanupUncertain means no owned-group termination attempt could be
	// confirmed. It is independent of the primary process failure.
	CleanupUncertain bool
	Executable       string
	Environment      map[string]string
}

type outputStream struct {
	hash   hash.Hash
	prefix bytes.Buffer
	length int64
	budget *captureBudget
}

type captureBudget struct {
	mu        sync.Mutex
	remaining int
	exceeded  bool
}

func (b *captureBudget) reserve(length int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	retained := min(length, b.remaining)
	b.remaining -= retained
	b.exceeded = b.exceeded || retained < length
	return retained
}

func newOutputStream(budget *captureBudget) *outputStream {
	return &outputStream{hash: sha256.New(), budget: budget}
}

func (s *outputStream) Write(data []byte) (int, error) {
	length := len(data)
	_, err := s.hash.Write(data)
	if err != nil {
		return 0, err
	}
	s.length += int64(length)
	retained := s.budget.reserve(length)
	if retained > 0 {
		_, err = s.prefix.Write(data[:retained])
		if err != nil {
			return 0, err
		}
	}
	return length, nil
}

func (s *outputStream) digest() string {
	return "sha256:" + hex.EncodeToString(s.hash.Sum(nil))
}

// Run uses the supplied argv directly, with no shell. The project root is the
// exact process cwd. A bounded host allowlist and explicit Go overrides form
// the child environment; caches and module files may still affect execution.
// A nonzero test exit, start failure or timeout is returned as a Result, not an
// error. Error identifies invalid runner input or a pre-start pipe setup
// failure; the latter also returns known no-start facts in Result.
func Run(ctx context.Context, request Request) (Result, error) {
	result := Result{}
	if !filepath.IsAbs(request.Root) || len(request.Command) == 0 || request.Command[0] == "" || request.Timeout <= 0 || request.MaxOutputBytes <= 0 {
		return result, fmt.Errorf("runner requires absolute root, nonempty argv, positive timeout and output limit")
	}
	environment, pinned, err := processEnvironment(request.Environment)
	if err != nil {
		return result, err
	}
	deadline, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	command := exec.CommandContext(deadline, request.Command[0], request.Command[1:]...)
	command.Dir = request.Root
	command.Env = environment
	command.WaitDelay = time.Second
	cleanup := &groupCleanup{}
	configureTermination(command, cleanup.terminate)
	budget := &captureBudget{remaining: request.MaxOutputBytes}
	stdout := newOutputStream(budget)
	stderr := newOutputStream(budget)
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return pipeSetupFailure(deadline, command, pinned, stdout, stderr, fmt.Errorf("stdout pipe: %w", err))
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return pipeSetupFailure(deadline, command, pinned, stdout, stderr, fmt.Errorf("stderr pipe: %w", err))
	}
	defer stdoutReader.Close()
	defer stderrReader.Close()
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	runErr := command.Start()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	drains := outputDrain{}
	if runErr == nil {
		stdoutDone := make(chan error, 1)
		stderrDone := make(chan error, 1)
		go drainOutputPipe(stdoutReader, stdout, stdoutDone)
		go drainOutputPipe(stderrReader, stderr, stderrDone)
		finished := make(chan struct{})
		watcherDone := make(chan error, 1)
		go func() {
			watcherDone <- watchProcessGroup(deadline, command, finished, cleanup.terminate)
		}()
		runErr = command.Wait()
		pipes := outputPipes{stdoutReader: stdoutReader, stderrReader: stderrReader, stdoutDone: stdoutDone, stderrDone: stderrDone}
		drains = awaitOutputPipes(pipes, command, command.WaitDelay, cleanup.terminate)
		// Wait reaps the direct child. Ordinary descendants with closed stdio
		// may still run; clean the group even when both pipes reached EOF.
		close(finished)
		<-watcherDone
		if !cleanup.confirmed() {
			_ = cleanup.terminate(command)
		}
	}
	result = Result{
		Started:             command.Process != nil,
		Failure:             failureKind(deadline, command, runErr),
		Stdout:              stdout.prefix.Bytes(),
		Stderr:              stderr.prefix.Bytes(),
		StdoutDigest:        stdout.digest(),
		StderrDigest:        stderr.digest(),
		StdoutBytes:         stdout.length,
		StderrBytes:         stderr.length,
		OutputExceeded:      budget.exceeded,
		StdoutDrainComplete: drains.stdoutComplete,
		StderrDrainComplete: drains.stderrComplete,
		CleanupUncertain:    cleanup.uncertain(command.Process != nil),
		Executable:          command.Path,
		Environment:         pinned,
	}
	result.Failure = cleanupFailure(result.Failure, result.CleanupUncertain)
	if (!drains.stdoutComplete || !drains.stderrComplete) && result.Failure == "" {
		result.Failure = "capture_incomplete"
	}
	result.PipeDrainUncertain = outputDrainUncertain(drains.stdoutComplete, drains.stderrComplete, deadline.Err(), result.Failure)
	if result.Started && command.ProcessState != nil && exitCodeReported(command) {
		exitCode := command.ProcessState.ExitCode()
		result.ExitCode = &exitCode
	}
	return result, nil
}

// No process has started when an owned output pipe cannot be created. Empty
// stream hashes account for the zero bytes observed; neither pipe reached EOF.
func pipeSetupFailure(ctx context.Context, command *exec.Cmd, pinned map[string]string, stdout, stderr *outputStream, setupErr error) (Result, error) {
	result := Result{
		Started:            false,
		Failure:            failureKind(ctx, command, setupErr),
		StdoutDigest:       stdout.digest(),
		StderrDigest:       stderr.digest(),
		PipeDrainUncertain: true,
		Executable:         command.Path,
		Environment:        pinned,
	}
	return result, setupErr
}

type outputPipes struct {
	stdoutReader *os.File
	stderrReader *os.File
	stdoutDone   <-chan error
	stderrDone   <-chan error
}

type outputDrain struct {
	stdoutComplete bool
	stderrComplete bool
}

type groupCleanup struct {
	mu        sync.Mutex
	attempted bool
	succeeded bool
}

func (c *groupCleanup) terminate(command *exec.Cmd) error {
	err := terminateProcessGroup(command)
	c.record(err)
	return err
}

func (c *groupCleanup) record(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempted = true
	c.succeeded = c.succeeded || err == nil
}

func (c *groupCleanup) confirmed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.succeeded
}

func (c *groupCleanup) uncertain(started bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return started && (!c.attempted || !c.succeeded)
}

func cleanupFailure(primary string, uncertain bool) string {
	if !uncertain || primary != "" {
		return primary
	}
	return "cleanup_failed"
}

func drainOutputPipe(reader *os.File, stream *outputStream, done chan<- error) {
	_, err := io.Copy(stream, reader)
	done <- err
}

func awaitOutputPipes(pipes outputPipes, command *exec.Cmd, delay time.Duration, terminate func(*exec.Cmd) error) outputDrain {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	timeout := timer.C
	stdoutDone := pipes.stdoutDone
	stderrDone := pipes.stderrDone
	stdoutForced := false
	stderrForced := false
	outcome := outputDrain{}
	for stdoutDone != nil || stderrDone != nil {
		select {
		case err := <-stdoutDone:
			outcome.stdoutComplete = err == nil && !stdoutForced
			stdoutDone = nil
		case err := <-stderrDone:
			outcome.stderrComplete = err == nil && !stderrForced
			stderrDone = nil
		case <-timeout:
			_ = terminate(command)
			if stdoutDone != nil {
				stdoutForced = true
				_ = pipes.stdoutReader.Close()
			}
			if stderrDone != nil {
				stderrForced = true
				_ = pipes.stderrReader.Close()
			}
			timeout = nil
		}
	}
	return outcome
}

func outputDrainUncertain(stdoutComplete, stderrComplete bool, contextErr error, failure string) bool {
	return !stdoutComplete || !stderrComplete || contextErr != nil || failure != ""
}

func watchProcessGroup(ctx context.Context, command *exec.Cmd, finished <-chan struct{}, terminate func(*exec.Cmd) error) error {
	select {
	case <-ctx.Done():
		return terminate(command)
	case <-finished:
		return nil
	}
}

func failureKind(ctx context.Context, command *exec.Cmd, runErr error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "canceled"
	}
	if command.Process == nil {
		return "start_failed"
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return "capture_incomplete"
	}
	if command.ProcessState == nil || command.ProcessState.ExitCode() < 0 {
		return "signal"
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return "wait_failed"
		}
	}
	return ""
}

func processEnvironment(overrides map[string]string) ([]string, map[string]string, error) {
	values := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "SystemRoot", "WINDIR", "USERPROFILE"} {
		value, present := os.LookupEnv(key)
		if present && value != "" {
			values[key] = value
		}
	}
	pinned := map[string]string{
		"CGO_ENABLED":  "0",
		"GOENV":        "off",
		"GOEXPERIMENT": "",
		"GOFIPS140":    "off",
		"GOFLAGS":      "",
		"GOPROXY":      "off",
		"GOSUMDB":      "off",
		"GOTOOLCHAIN":  "local",
		"GOWORK":       "off",
	}
	for key, value := range overrides {
		if !validOverride(key, value) {
			return nil, nil, fmt.Errorf("invalid runner environment override %q", key)
		}
		pinned[key] = value
	}
	for key, value := range pinned {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment, pinned, nil
}

func validOverride(key, value string) bool {
	if strings.ContainsRune(value, '\x00') || !environmentKey.MatchString(key) {
		return false
	}
	return key == "CGO_ENABLED" || strings.HasPrefix(key, "GO")
}
