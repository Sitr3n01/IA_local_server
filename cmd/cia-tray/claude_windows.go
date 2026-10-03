//go:build windows

package main

import (
	"context"

	"github.com/sitr3n/local-ai-provider/internal/claudedesktop"
)

// discoverClaudeDesktop finds the installed Claude Desktop package and builds
// the manager that opens its two instances. When it cannot, the service is nil
// and the detail says why.
func discoverClaudeDesktop(backupDir string) (claudeDesktopService, string) {
	identity, paths, err := claudedesktop.DiscoverWindows(context.Background(), backupDir)
	if err != nil {
		return nil, "Claude Desktop indisponível: " + sanitizeClaudeDetail(err)
	}
	desktop, err := claudedesktop.NewWindowsDesktop(identity)
	if err != nil {
		return nil, "Configuração Claude inválida: " + sanitizeClaudeDetail(err)
	}
	manager, err := claudedesktop.NewManager(paths, claudedesktop.NewWindowsPolicyReader(), desktop, claudedesktop.NewDPAPIProtector())
	if err != nil {
		return nil, "Gerenciador Claude indisponível: " + sanitizeClaudeDetail(err)
	}
	return manager, ""
}
