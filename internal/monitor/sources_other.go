//go:build !windows

package monitor

// Discovery reads the kernel's process and socket tables, which this build does
// not: off Windows the page shows the edge and no other source.
func newHostView() hostView { return nil }
