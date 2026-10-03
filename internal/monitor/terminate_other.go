//go:build !windows

package monitor

// Ending another program's process is done with the Windows API only; elsewhere
// the page offers no such action.
func newTerminator() processTerminator { return nil }
