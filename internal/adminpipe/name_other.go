//go:build !windows

package adminpipe

// DefaultName reports no administrative pipe off Windows, where the DACL that
// authenticates the transport does not exist.
func DefaultName(string) string { return "" }
