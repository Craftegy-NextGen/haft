//go:build !windows

package initfs

// volumeRootResourcesAllowed reports whether a filesystem root may be a
// publication coordination resource. Outside Windows it never may.
const volumeRootResourcesAllowed = false
