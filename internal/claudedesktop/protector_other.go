//go:build !windows

package claudedesktop

import "errors"

// DPAPIProtector intentionally cannot be used outside Windows. Tests inject a
// fake protector; production desktop state is Windows-only.
type DPAPIProtector struct{}

func NewDPAPIProtector() DPAPIProtector { return DPAPIProtector{} }

func (DPAPIProtector) Protect([]byte) ([]byte, error) {
	return nil, errors.New("Windows DPAPI is unavailable on this platform")
}

func (DPAPIProtector) Unprotect([]byte) ([]byte, error) {
	return nil, errors.New("Windows DPAPI is unavailable on this platform")
}
