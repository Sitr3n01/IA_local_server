//go:build windows

package claudedesktop

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPIProtector protects backups to the current Windows user. It does not use
// machine scope, so a copied backup cannot be decrypted by another account.
type DPAPIProtector struct{}

func NewDPAPIProtector() DPAPIProtector { return DPAPIProtector{} }

func (DPAPIProtector) Protect(plain []byte) ([]byte, error) {
	return protectDPAPI(plain, true)
}

func (DPAPIProtector) Unprotect(protected []byte) ([]byte, error) {
	return protectDPAPI(protected, false)
}

func protectDPAPI(input []byte, protect bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, errors.New("DPAPI input cannot be empty")
	}
	in := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
