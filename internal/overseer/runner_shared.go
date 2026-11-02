//go:build windows

package overseer

// ReviewerRunResult - defined here for Windows builds since runner.go is Unix-only
type ReviewerRunResult struct {
	Input      ReviewResultInput
	PromptPath string
	PacketPath string
	ResultPath string
	Stdout     string
	Abstained  bool
}