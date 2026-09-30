//go:build windows

package monitor

import (
	"strings"
	"testing"
	"time"
)

func TestApprovalTextForAStopNamesTheProcessAndSaysItBypassesTheEdge(t *testing.T) {
	text := approvalText(approvalRequest{
		Environment: "canary", Action: actionStopSource, DisplayName: "LM Studio / Bionic",
		Target: "llama-server.exe · PID 6388", Detail: ", 7,2 GiB de VRAM",
	}, 45*time.Second)
	for _, want := range []string{"Encerrar o processo llama-server.exe · PID 6388", "LM Studio / Bionic", "7,2 GiB de VRAM", "NÃO passa pelo cia-edge", "45 s", "interrompe qualquer geração"} {
		if !strings.Contains(text, want) {
			t.Errorf("the dialog does not say %q:\n%s", want, text)
		}
	}
	// The wording about the administrative pipe belongs to the edge's actions and
	// would be false here.
	if strings.Contains(text, "pipe administrativo") {
		t.Errorf("a stop was described as going to the pipe:\n%s", text)
	}
}

func TestApprovalTextForAnEdgeActionIsUnchanged(t *testing.T) {
	text := approvalText(approvalRequest{Environment: "canary", Action: actionUnload, DisplayName: "Gemma", Model: "gemma"}, 45*time.Second)
	if !strings.Contains(text, "Descarregar o modelo") || !strings.Contains(text, "pipe administrativo do cia-edge") {
		t.Errorf("the edge's own confirmation changed:\n%s", text)
	}
}
