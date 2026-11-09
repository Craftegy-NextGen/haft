//go:build ignore

// The Windows body of the shared sidecar now lives in sidecar_shared_windows.go
// (ADR-095); this upstream stub is kept, excluded from every build, so that an
// upstream change to it still merges.

package embedding

import "errors"

func newSharedSidecarAdapter(_ string, _ sidecarSpec) (Embedder, error) {
	return nil, errors.New("shared embedding sidecar is unsupported on this platform")
}
