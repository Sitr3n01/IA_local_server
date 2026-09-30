//go:build !windows

package main

import "errors"

func openBrowser(string) error {
	return errors.New("opening a browser is supported on Windows only; open the page address by hand")
}
