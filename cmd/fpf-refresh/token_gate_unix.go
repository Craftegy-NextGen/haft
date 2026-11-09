//go:build !windows

package main

import "github.com/m0n0x41d/haft/internal/fpfrefresh"

// tokenGateVerifier turns the token-gate coordinates the check names into the
// verifier it runs. Outside Windows that is gate itself: /bin/bash running
// scripts/fpf_query_token_gate.sh.
func tokenGateVerifier(gate fpfrefresh.ExecutableCandidateTokenGate) fpfrefresh.CandidateTokenGate {
	return gate
}
