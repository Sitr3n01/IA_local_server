//go:build windows

package monitor

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The confirmation is a native message box owned by this process, not by the
// page. Script running in the page - an injected one included - can ask for an
// operation but can neither answer, suppress nor forge this dialog, which is
// the second factor the credential-free pipe does not provide (ADR 0018,
// control 1). Cancel has the default focus, and the box closes itself as
// declined after approvalTimeout.
//
// MessageBoxTimeoutW is exported by user32 on every supported Windows but is not
// documented; when it cannot be found the documented MessageBoxW is used and the
// dialog simply waits for an answer.
var (
	user32DLL              = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxTimeoutW = user32DLL.NewProc("MessageBoxTimeoutW")
	procMessageBoxW        = user32DLL.NewProc("MessageBoxW")
)

// approvalTimeout is how long a confirmation dialog stays open. An approval
// given long after the request describes a moment that has passed.
const approvalTimeout = 45 * time.Second

const (
	mbOKCancel      = 0x00000001
	mbIconWarning   = 0x00000030
	mbDefButton2    = 0x00000100
	mbSetForeground = 0x00010000
	mbTopMost       = 0x00040000
	idOK            = 1
	mbTimedOut      = 32000
)

type dialogApprover struct {
	timeout time.Duration
}

func newApprover() approver { return dialogApprover{timeout: approvalTimeout} }

func (a dialogApprover) approve(request approvalRequest) approval {
	text, err := windows.UTF16PtrFromString(approvalText(request, a.timeout))
	if err != nil {
		return approvalDeclined
	}
	title, err := windows.UTF16PtrFromString("IA Local — confirmar operação")
	if err != nil {
		return approvalDeclined
	}
	flags := uintptr(mbOKCancel | mbIconWarning | mbDefButton2 | mbTopMost | mbSetForeground)

	// The box belongs to the thread that creates it and pumps its messages
	// there, so the goroutine stays on that thread until it is answered.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var result uintptr
	if procMessageBoxTimeoutW.Find() == nil {
		result, _, _ = procMessageBoxTimeoutW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)),
			flags, 0, uintptr(a.timeout.Milliseconds()))
	} else {
		result, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), flags)
	}
	switch result {
	case idOK:
		return approvalGranted
	case mbTimedOut:
		return approvalExpired
	default:
		return approvalDeclined
	}
}

func approvalText(request approvalRequest, timeout time.Duration) string {
	if request.Action == actionStopSource {
		return fmt.Sprintf("O monitor do IA Local (%s) pede para:\r\n\r\n"+
			"    Encerrar o processo %s\r\n    (%s%s)\r\n\r\n"+
			"Isso descarrega o modelo que essa ferramenta carregou e interrompe qualquer geração em andamento. "+
			"A ferramenta pode recarregá-lo depois ou mostrar um erro.\r\n\r\n"+
			"Esta ação NÃO passa pelo cia-edge: o monitor encerra o processo diretamente, com as permissões do seu usuário. Permitir?\r\n"+
			"Sem resposta em %d s, nada é feito.",
			request.Environment, request.Target, request.DisplayName, request.Detail, int(timeout.Seconds()))
	}
	var action, consequence string
	switch request.Action {
	case actionUnload:
		action = "Descarregar o modelo"
		consequence = "O processo do modelo será encerrado e a VRAM, liberada."
	default:
		action = "Carregar o modelo"
		consequence = "O modelo será carregado agora."
		if request.Replaces != "" {
			consequence = fmt.Sprintf("%q será descarregado antes.", request.Replaces)
		}
	}
	return fmt.Sprintf("O monitor do IA Local (%s) pede para:\r\n\r\n"+
		"    %s %q\r\n    (%s)\r\n\r\n"+
		"%s\r\n\r\n"+
		"O pedido vai ao pipe administrativo do cia-edge. Permitir?\r\n"+
		"Sem resposta em %d s, nada é feito.",
		request.Environment, action, request.DisplayName, request.Model, consequence, int(timeout.Seconds()))
}
