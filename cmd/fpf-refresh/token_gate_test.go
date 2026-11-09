package main

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/m0n0x41d/haft/internal/fpfrefresh"
)

// TestTokenGateVerifierIsIdentityOutsideWindows: Linux and macOS run the
// coordinates the check names (/bin/bash and the upstream script) unchanged;
// only Windows swaps in the uv wrapper script and the resolved bash, or a
// verifier that explains why no bash was found.
func TestTokenGateVerifierIsIdentityOutsideWindows(t *testing.T) {
	gate := fpfrefresh.ExecutableCandidateTokenGate{
		ShellPath:  "/bin/bash",
		ScriptPath: filepath.Join(t.TempDir(), "scripts", "fpf_query_token_gate.sh"),
	}
	got := tokenGateVerifier(gate)
	if runtime.GOOS != "windows" {
		if got != fpfrefresh.CandidateTokenGate(gate) {
			t.Fatalf("tokenGateVerifier(%+v) = %+v, want the same gate", gate, got)
		}
		return
	}
	switch verifier := got.(type) {
	case fpfrefresh.ExecutableCandidateTokenGate:
		if base := filepath.Base(verifier.ScriptPath); base != "fpf_query_token_gate_uv.sh" {
			t.Fatalf("Windows script = %q, want the uv wrapper", verifier.ScriptPath)
		}
	default:
		if err := got.VerifyCandidate(context.Background(), fpfrefresh.CandidateTokenGateInput{}); err == nil {
			t.Fatalf("a Windows verifier without a shell must fail, got %T", got)
		}
	}
}
