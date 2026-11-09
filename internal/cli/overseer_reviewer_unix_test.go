//go:build !windows

package cli

import "testing"

// reviewerWritingResult is the reviewer_command of the overseer tests that
// need a reviewer to report one result: a POSIX shell command that writes the
// payload to the result file (ADR-093 E3, E6, extracted from the tests'
// inline command strings). Its Windows sibling re-executes the test binary.
func reviewerWritingResult(t *testing.T, payload string) string {
	t.Helper()
	return "printf '%s' '" + payload + "' > \"$HAFT_OVERSEER_RESULT_FILE\""
}
