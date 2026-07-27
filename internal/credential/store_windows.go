//go:build windows

package credential

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

type nativeCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredRead   = advapi32.NewProc("CredReadW")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

func Read(name string) (string, error) {
	target, err := Target(name)
	if err != nil {
		return "", err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", err
	}
	var native *nativeCredential
	r1, _, callErr := procCredRead.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&native)),
	)
	if r1 == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return "", ErrNotFound
		}
		return "", callErr
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(native)))
	if native.CredentialBlob == nil || native.CredentialBlobSize == 0 {
		return "", ErrNotFound
	}
	blob := unsafe.Slice(native.CredentialBlob, int(native.CredentialBlobSize))
	value := string(blob)
	runtime.KeepAlive(native)
	return value, nil
}

func Write(name, value string) error {
	target, err := Target(name)
	if err != nil {
		return err
	}
	if value == "" {
		return errors.New("credential value is empty")
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	userPtr, err := windows.UTF16PtrFromString("CIA Local AI")
	if err != nil {
		return err
	}
	blob := []byte(value)
	native := nativeCredential{
		Type:               credTypeGeneric,
		TargetName:         targetPtr,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
		UserName:           userPtr,
	}
	r1, _, callErr := procCredWrite.Call(uintptr(unsafe.Pointer(&native)), 0)
	for i := range blob {
		blob[i] = 0
	}
	runtime.KeepAlive(native)
	if r1 == 0 {
		return callErr
	}
	return nil
}

func Delete(name string) error {
	target, err := Target(name)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r1, _, callErr := procCredDelete.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
	)
	if r1 == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return ErrNotFound
		}
		return callErr
	}
	return nil
}
