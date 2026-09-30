//go:build !windows

package main

import (
	"fmt"
	"os"
)

// main is a stub on every platform but Windows. cia-console hosts a WebView2
// control, which only exists on Windows; the portable bridge.go core still
// builds and its tests still run here (that is the point of the ubuntu-
// latest "race" CI job), but there is nothing this binary can do at runtime.
func main() {
	fmt.Fprintln(os.Stderr, "cia-console is a Windows-only WebView2 operator console host; it does not run on this platform.")
	os.Exit(1)
}
