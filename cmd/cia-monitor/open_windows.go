//go:build windows

package main

import "golang.org/x/sys/windows"

// openBrowser hands the page to the shell, which opens the default browser. The
// URL is built from the validated loopback listen address, never from input.
func openBrowser(page string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(page)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
