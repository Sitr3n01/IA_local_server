//go:build !windows

package claudedesktop

import (
	"context"
	"errors"
)

func DiscoverWindows(context.Context, string) (Identity, Paths, error) {
	return Identity{}, Paths{}, errors.New("Claude Desktop MSIX discovery is only available on Windows")
}

func NewWindowsPolicyReader() PolicyReader { return unavailablePolicy{} }

func NewWindowsDesktop(Identity) (Desktop, error) {
	return nil, errors.New("Claude Desktop MSIX control is only available on Windows")
}

type unavailablePolicy struct{}

func (unavailablePolicy) ManagedInference(context.Context) (bool, error) {
	return false, errors.New("Claude Desktop MSIX policy lookup is only available on Windows")
}
