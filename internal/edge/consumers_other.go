//go:build !windows

package edge

// topMemoryConsumers has no process table to read away from Windows; a
// refusal then gives the numbers without naming applications.
func topMemoryConsumers(int) []memoryConsumer { return nil }
