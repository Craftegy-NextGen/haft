package overseer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Windows counterparts of the tests in runner_unix_test.go (ADR-093 E3, E6):
// the reviewer commands there are POSIX shell scripts, so here the reviewer is
// the test binary re-executed through cmd.exe.

const overseerHelperEnv = "HAFT_OVERSEER_TEST_HELPER"

var reviewerPayloads = map[string]string{
	"runner": `{"findings":[{"id":"ofind-runner","severity":"high","confidence":"high","claim":"Runner found an invariant violation.","concrete_harm":"The agent would miss the review signal."}]}`,
	"alias":  `{"findings":[{"id":"ofind-alias","severity":"medium","title":"Smoke reviewer finding","description":"This proves the reviewer result was ingested from the post-commit hook.","recommendation":"Close this finding with a disposition after the smoke test."}]}`,
}

// TestOverseerReviewerHelper is not a test: reviewer commands re-execute the
// test binary to run one of these modes.
func TestOverseerReviewerHelper(t *testing.T) {
	if os.Getenv(overseerHelperEnv) != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index < 0 || index+2 >= len(os.Args) {
		os.Exit(2)
	}
	argument := os.Args[index+2]
	switch os.Args[index+1] {
	case "write-result":
		if err := os.WriteFile(os.Getenv("HAFT_OVERSEER_RESULT_FILE"), []byte(reviewerPayloads[argument]), 0o644); err != nil {
			os.Exit(4)
		}
	case "spawn-reviewer":
		// The nested reviewer keeps the shell's output pipe, as `sh -c` does
		// in the Unix test.
		reviewer := exec.Command(os.Args[0], "-test.run=^TestOverseerReviewerHelper$", "--", "reviewer", argument)
		reviewer.Stdout = os.Stdout
		reviewer.Stderr = os.Stderr
		if err := reviewer.Run(); err != nil {
			os.Exit(5)
		}
	case "reviewer":
		// A reviewer that ignores the graceful CTRL_BREAK step.
		signal.Ignore(os.Interrupt)
		if err := os.WriteFile(filepath.Join(argument, "ready"), []byte(fmt.Sprint(os.Getpid())), 0o644); err != nil {
			os.Exit(6)
		}
		time.Sleep(4 * time.Second)
		_ = os.WriteFile(filepath.Join(argument, "survived"), []byte("survived"), 0o644)
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

// helperReviewerCommand is a cmd.exe command line that runs the helper mode.
func helperReviewerCommand(t *testing.T, mode, argument string) string {
	t.Helper()
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(overseerHelperEnv, "1")
	return fmt.Sprintf(`"%s" "-test.run=^TestOverseerReviewerHelper$" -- %s "%s"`, binary, mode, argument)
}

func windowsReviewerFixture(t *testing.T) (string, StoredRun, Config) {
	t.Helper()
	root := t.TempDir()
	packet, err := BuildPacket(BuildInput{
		Producer: DefaultProducer("test"),
		Subject: Subject{
			Kind:     "commit",
			Ref:      "HEAD",
			SHA:      "abc123",
			DiffHash: "sha256:diff",
		},
		RepoState: RepoState{GitRoot: root, Branch: "main"},
		ChangedFiles: []ChangedFile{{
			Path:   "internal/cli/init.go",
			Status: "modified",
		}},
		Budget: DefaultContextBudget(),
	})
	if err != nil {
		t.Fatalf("BuildPacket returned error: %v", err)
	}
	stored := StoredRun{
		Packet: packet,
		Run:    NewDeterministicReviewRun(packet, "2026-06-09T00:00:00Z"),
	}
	config := DefaultConfig()
	config.LLMReview = "on"
	config.ReviewerAgent = "command"
	return root, stored, config
}

func TestRunConfiguredReviewerReadsCommandResultFileWindows(t *testing.T) {
	root, stored, config := windowsReviewerFixture(t)
	config.ReviewerCommand = helperReviewerCommand(t, "write-result", "runner")

	result, err := RunConfiguredReviewer(context.Background(), root, config, stored)
	if err != nil {
		t.Fatalf("RunConfiguredReviewer returned error: %v", err)
	}
	if result.PromptPath == "" || result.PacketPath == "" || result.ResultPath == "" {
		t.Fatalf("runner paths were not populated: %+v", result)
	}
	if got := result.Input.Reviewer.Agent; got != "command" {
		t.Fatalf("reviewer agent = %q, want command", got)
	}
	if len(result.Input.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Input.Findings))
	}

	stored, err = IngestReviewResult(stored, result.Input, "2026-06-09T01:00:00Z")
	if err != nil {
		t.Fatalf("IngestReviewResult returned error: %v", err)
	}
	if stored.Run.Findings[0].CountsForREff {
		t.Fatalf("runner finding must stay advisory and not count for R_eff")
	}
	if !strings.Contains(FormatStatusSignals(BuildStatusSummary(stored, true, MaintenanceRun{}, false)), "Runner found an invariant violation") {
		t.Fatalf("runner finding did not surface in status")
	}
}

func TestRunConfiguredReviewerMapsHumanReadableFindingAliasesWindows(t *testing.T) {
	root, stored, config := windowsReviewerFixture(t)
	config.ReviewerCommand = helperReviewerCommand(t, "write-result", "alias")

	result, err := RunConfiguredReviewer(context.Background(), root, config, stored)
	if err != nil {
		t.Fatalf("RunConfiguredReviewer returned error: %v", err)
	}

	stored, err = IngestReviewResult(stored, result.Input, "2026-06-09T01:00:00Z")
	if err != nil {
		t.Fatalf("IngestReviewResult returned error: %v", err)
	}
	if stored.Run.Verdict != "findings_recorded" {
		t.Fatalf("verdict = %q, want findings_recorded", stored.Run.Verdict)
	}
	if len(stored.Run.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(stored.Run.Findings))
	}
	finding := stored.Run.Findings[0]
	if finding.Claim != "Smoke reviewer finding" {
		t.Fatalf("claim = %q, want alias title", finding.Claim)
	}
	if finding.ConcreteHarm != "This proves the reviewer result was ingested from the post-commit hook." {
		t.Fatalf("concrete harm = %q, want alias description", finding.ConcreteHarm)
	}
	if finding.MinimalFix != "Close this finding with a disposition after the smoke test." {
		t.Fatalf("minimal fix = %q, want alias recommendation", finding.MinimalFix)
	}
}

// The phase-1 runner ended only cmd.exe on timeout and left the reviewer it
// started running; the reviewer must be dead once the timeout returns.
func TestRunConfiguredReviewerTimeoutKillsNestedReviewer(t *testing.T) {
	root, stored, config := windowsReviewerFixture(t)
	markers := t.TempDir()
	config.ReviewTimeoutSeconds = 1
	config.ReviewerCommand = helperReviewerCommand(t, "spawn-reviewer", markers)

	started := time.Now()
	_, err := RunConfiguredReviewer(context.Background(), root, config, stored)
	if err == nil {
		t.Fatalf("RunConfiguredReviewer should time out")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s, want under 5s", elapsed)
	}
	if _, err := os.Stat(filepath.Join(markers, "ready")); err != nil {
		t.Fatalf("nested reviewer never ran: %v", err)
	}
	// The reviewer writes its survival marker 4 seconds after it is ready.
	time.Sleep(5 * time.Second)
	if _, err := os.Stat(filepath.Join(markers, "survived")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nested reviewer survived the timeout: %v", err)
	}
}

func TestReviewerShellCommandLineKeepsCommandQuoting(t *testing.T) {
	got := reviewerShellCommandLine(`C:\Windows\System32\cmd.exe`, `"C:\Program Files\tool.exe" --flag "a b"`)
	want := `C:\Windows\System32\cmd.exe /d /s /c ""C:\Program Files\tool.exe" --flag "a b""`
	if got != want {
		t.Fatalf("command line = %s, want %s", got, want)
	}
}
