package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const (
	overseerReviewerHelperEnv  = "HAFT_CLI_OVERSEER_REVIEWER_HELPER"
	overseerReviewerPayloadEnv = "HAFT_CLI_OVERSEER_REVIEWER_PAYLOAD"
)

// TestCLIOverseerReviewerHelper is not a test: reviewerWritingResult commands
// re-execute the test binary to run it as the reviewer (ADR-093 E6).
func TestCLIOverseerReviewerHelper(t *testing.T) {
	if os.Getenv(overseerReviewerHelperEnv) != "1" {
		return
	}
	err := os.WriteFile(
		os.Getenv("HAFT_OVERSEER_RESULT_FILE"),
		[]byte(os.Getenv(overseerReviewerPayloadEnv)),
		0o644,
	)
	if err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

// reviewerWritingResult is the Windows sibling of the POSIX shell command in
// overseer_reviewer_unix_test.go: a cmd.exe command line that runs the test
// binary as a reviewer, which writes the payload (passed through the
// environment, so cmd.exe quoting never touches the JSON) to the result file.
func reviewerWritingResult(t *testing.T, payload string) string {
	t.Helper()
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(overseerReviewerHelperEnv, "1")
	t.Setenv(overseerReviewerPayloadEnv, payload)
	return fmt.Sprintf(`"%s" "-test.run=^TestCLIOverseerReviewerHelper$"`, binary)
}
