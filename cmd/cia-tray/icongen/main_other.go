//go:build !windows

// Command icongen renders the IA Local icon with GDI+ and therefore only runs
// on Windows, where cia-tray.exe is built.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "icongen: Windows only")
	os.Exit(1)
}
