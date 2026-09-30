//go:build windows

package main

import (
	"context"

	"github.com/sitr3n/local-ai-provider/internal/claudedesktop"
)

type windowsClaudeDesktopService struct {
	manager  *claudedesktop.Manager
	identity claudedesktop.Identity
}

func (s windowsClaudeDesktopService) CurrentMode() (claudedesktop.Mode, error) {
	return s.manager.CurrentMode()
}

func (s windowsClaudeDesktopService) Apply(ctx context.Context, mode claudedesktop.Mode, gateway claudedesktop.Gateway) error {
	return s.manager.Apply(ctx, mode, gateway)
}

func (s windowsClaudeDesktopService) Launch(ctx context.Context) error {
	return claudedesktop.LaunchWindows(ctx, s.identity)
}

// discoverClaudeDesktop finds the installed Claude Desktop package and builds
// the manager that switches its 3P mode. When it cannot, the service is nil and
// the detail says why.
func discoverClaudeDesktop(backupDir string) (claudeDesktopService, string) {
	identity, paths, err := claudedesktop.DiscoverWindows(context.Background(), backupDir)
	if err != nil {
		return nil, "Claude Desktop indisponível: " + sanitizeClaudeDetail(err)
	}
	verifier, err := claudedesktop.NewGatewayVerifier(paths, identity)
	if err != nil {
		return nil, "Configuração Claude inválida: " + sanitizeClaudeDetail(err)
	}
	manager, err := claudedesktop.NewManager(paths, identity, claudedesktop.NewWindowsPolicyReader(), claudedesktop.NewWindowsRestarter(), verifier, claudedesktop.NewDPAPIProtector())
	if err != nil {
		return nil, "Gerenciador Claude indisponível: " + sanitizeClaudeDetail(err)
	}
	return windowsClaudeDesktopService{manager: manager, identity: identity}, ""
}
