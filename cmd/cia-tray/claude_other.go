//go:build !windows

package main

// Both Claude Desktop instances open through its Windows package, so away
// from Windows the tray has no Claude Desktop service.
func discoverClaudeDesktop(string) (claudeDesktopService, string) {
	return nil, "Claude Desktop is managed only on Windows"
}
