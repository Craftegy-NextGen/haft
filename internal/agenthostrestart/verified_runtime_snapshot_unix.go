//go:build darwin || linux || windows

package agenthostrestart

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

func (store Store) loadVerifiedRuntimeSnapshotUnlocked() (
	VerifiedRuntimeSnapshot,
	error,
) {
	checkpoint, err := store.loadUnlocked()
	if err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	if checkpoint.state != StateVerified {
		return VerifiedRuntimeSnapshot{}, fmt.Errorf(
			"restart checkpoint is %s, want verified",
			checkpoint.state.String(),
		)
	}
	checkpointRaw, err := readPrivateRegularNoFollow(
		store.checkpointPath,
		MaximumCheckpointBytes,
		"restart checkpoint",
	)
	if err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	checkpointCanonical, err := checkpoint.CanonicalBytes()
	if err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	if !bytes.Equal(checkpointRaw, checkpointCanonical) {
		return VerifiedRuntimeSnapshot{}, fmt.Errorf(
			"verified restart checkpoint is not canonical",
		)
	}
	liveReceipt, liveRaw, err := store.loadCanonicalLiveReceipt(checkpoint)
	if err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	fallbackReceipt, fallbackRaw, err := store.loadCanonicalFallbackReceipt(
		checkpoint,
	)
	if err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	if err := store.validateReservedCandidateDigest(
		checkpoint.desiredHaftBinaryDigest,
	); err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	if err := store.requireTemporaryStagesAbsent(); err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	return VerifiedRuntimeSnapshot{
		RestartID:                      checkpoint.restartID,
		ThreadID:                       checkpoint.threadID,
		ResumeIntentDigest:             checkpoint.resumeIntentDigest,
		CheckpointState:                checkpoint.state.String(),
		CheckpointAttempt:              checkpoint.attempt,
		CheckpointCreatedAt:            checkpoint.createdAt,
		RestartCheckpointDigest:        digestRestartEvidence(checkpointRaw),
		PreparedTaskRuntimePID:         checkpoint.taskRuntime.PID,
		PreparedTaskRuntimeStartedAt:   checkpoint.taskRuntime.StartedAt,
		PreparedTaskRuntimeExecutable:  checkpoint.taskRuntime.ExecutablePath,
		PreparedTaskRuntimeArgsDigest:  checkpoint.taskRuntime.ArgumentsDigest,
		InstalledExecutablePath:        checkpoint.expectedHaftBinaryPath,
		InstalledExecutableDigest:      checkpoint.desiredHaftBinaryDigest,
		ProjectRoot:                    checkpoint.repositoryRoot,
		LiveMCPPID:                     liveReceipt.PID,
		LiveMCPStartedAt:               liveReceipt.ProcessStartedAt,
		LiveMCPFulfilledAt:             liveReceipt.FulfilledAt,
		LiveMCPExecutablePath:          liveReceipt.ExecutablePath,
		LiveMCPExecutableDigest:        liveReceipt.ExecutableDigest,
		LiveMCPProjectRoot:             liveReceipt.ProjectRoot,
		LiveMCPReceiptDigest:           digestRestartEvidence(liveRaw),
		FallbackReceiptDigest:          digestRestartEvidence(fallbackRaw),
		FallbackWakeCount:              fallbackReceipt.WakeCount,
		FallbackClearedAt:              fallbackReceipt.ClearedAt,
		ExactTaskResumeCount:           1,
		SingleWriterObserved:           true,
		LaunchdRemovalObserved:         true,
		PrivateStateGitignoredObserved: true,
		CandidateDigestReserved:        true,
		TemporaryStagesAbsent:          true,
	}, nil
}

func (store Store) loadCanonicalLiveReceipt(
	checkpoint Checkpoint,
) (LiveMCPReceipt, []byte, error) {
	receipt, err := store.loadLiveMCPReceiptUnlocked(checkpoint)
	if err != nil {
		return LiveMCPReceipt{}, nil, err
	}
	path := store.restartReceiptPath(
		checkpoint.restartID,
		liveMCPReceiptSuffix,
	)
	raw, err := readPrivateRestartJSON(path)
	if err != nil {
		return LiveMCPReceipt{}, nil, err
	}
	wire := liveMCPReceiptWire{
		RestartID:             receipt.RestartID,
		CheckpointBasisDigest: receipt.CheckpointBasisDigest,
		Nonce:                 receipt.Nonce,
		PID:                   receipt.PID,
		ExecutablePath:        receipt.ExecutablePath,
		ExecutableDigest:      receipt.ExecutableDigest,
		ProjectRoot:           receipt.ProjectRoot,
		ProcessStartedAt:      receipt.ProcessStartedAt.UTC().Format(time.RFC3339Nano),
		FulfilledAt:           receipt.FulfilledAt.UTC().Format(time.RFC3339Nano),
	}
	canonical, err := canonicalRestartJSON(wire)
	if err != nil {
		return LiveMCPReceipt{}, nil, err
	}
	if !bytes.Equal(raw, canonical) {
		return LiveMCPReceipt{}, nil, fmt.Errorf(
			"verified live MCP receipt is not canonical",
		)
	}
	return receipt, raw, nil
}

func (store Store) loadCanonicalFallbackReceipt(
	checkpoint Checkpoint,
) (ResumeFallbackReceipt, []byte, error) {
	receipt, err := store.LoadResumeFallbackReceipt(checkpoint)
	if err != nil {
		return ResumeFallbackReceipt{}, nil, err
	}
	path := store.restartReceiptPath(
		checkpoint.restartID,
		fallbackReceiptSuffix,
	)
	raw, err := readPrivateRestartJSON(path)
	if err != nil {
		return ResumeFallbackReceipt{}, nil, err
	}
	wire := fallbackReceiptWire{
		RestartID:             receipt.RestartID,
		CheckpointBasisDigest: receipt.CheckpointBasisDigest,
		Nonce:                 receipt.Nonce,
		WakeCount:             receipt.WakeCount,
		ClearedAt:             receipt.ClearedAt.UTC().Format(time.RFC3339Nano),
	}
	canonical, err := canonicalRestartJSON(wire)
	if err != nil {
		return ResumeFallbackReceipt{}, nil, err
	}
	if !bytes.Equal(raw, canonical) {
		return ResumeFallbackReceipt{}, nil, fmt.Errorf(
			"verified resume fallback receipt is not canonical",
		)
	}
	return receipt, raw, nil
}

func (store Store) requireTemporaryStagesAbsent() error {
	entries, err := os.ReadDir(store.directoryPath)
	if err != nil {
		return fmt.Errorf("inspect restart temporary stages: %w", err)
	}
	for _, entry := range entries {
		if isRestartTemporaryStage(entry.Name()) {
			return fmt.Errorf(
				"restart temporary stage %q remains",
				entry.Name(),
			)
		}
	}
	return nil
}

func isRestartTemporaryStage(name string) bool {
	prefixes := []string{
		stagePrefix,
		restartReceiptStagePrefix,
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func digestRestartEvidence(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}
