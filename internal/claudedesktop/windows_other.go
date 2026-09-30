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
func NewWindowsRestarter() Restarter       { return unavailableRestarter{} }
func LaunchWindows(context.Context, Identity) error {
	return errors.New("Claude Desktop MSIX launch is only available on Windows")
}

type unavailablePolicy struct{}

func (unavailablePolicy) ManagedInference(context.Context) (bool, error) {
	return false, errors.New("Claude Desktop MSIX policy lookup is only available on Windows")
}

type unavailableRestarter struct{}

func (unavailableRestarter) Restart(context.Context, Identity) error {
	return errors.New("Claude Desktop MSIX relaunch is only available on Windows")
}
