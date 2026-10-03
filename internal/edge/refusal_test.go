package edge

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCapacityRefusalNamesTheShortfallAndWhatToClose(t *testing.T) {
	required, headroom, reclaim := 20.2, 17.93, 9.94
	text := capacityRefusal(capacityStatus{
		Model:                  "qwen36-35b-a3b-huge-256k",
		Reason:                 "insufficient_physical_memory",
		RequiredPhysicalGiB:    &required,
		PhysicalHeadroomGiB:    &headroom,
		ReservePhysicalGiB:     physicalReserveGiB,
		ReclaimablePhysicalGiB: &reclaim,
	}, []memoryConsumer{{"claude", 3.59}, {"firefox", 2.99}, {"RazerAppEngine", 1.17}})
	if !strings.Contains(text, "os que mais usam RAM são") {
		t.Errorf("a physical refusal does not say the list is resident memory:\n%s", text)
	}
	for _, want := range []string{
		"qwen36-35b-a3b-huge-256k",
		"precisa de 20,2 GiB (com 2,0 GiB de reserva)",
		"há 17,9 GiB, já contando os 9,9 GiB que descarregar o modelo atual libera",
		"Faltam 2,3 GiB",
		"claude 3,6 GiB, firefox 3,0 GiB e RazerAppEngine 1,2 GiB",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal lacks %q:\n%s", want, text)
		}
	}
}

func TestCapacityRefusalWithoutNumbersFallsBackToTheReason(t *testing.T) {
	text := capacityRefusal(capacityStatus{Reason: "insufficient_physical_memory"}, []memoryConsumer{{"firefox", 3}})
	if text != capacityMessage("insufficient_physical_memory") {
		t.Fatalf("refusal without measurements = %q", text)
	}
	vram, device := 15.49, 15.92
	text = capacityRefusal(capacityStatus{Model: "big", Reason: "insufficient_vram_budget", RequiredVRAMGiB: &vram, DeviceVRAMGiB: &device, ReserveVRAMGiB: vramReserveGiB}, nil)
	if !strings.Contains(text, "pede 15,5 GiB de VRAM") || !strings.Contains(text, "a GPU tem 15,9 GiB") {
		t.Fatalf("VRAM refusal = %q", text)
	}
}

// refusedServer admits nothing for lack of physical memory: an 18.2 GiB
// resident model on a host with 17.9 GiB free.
func refusedServer(t *testing.T, consumers func(int, bool) []memoryConsumer) (*Server, func() int64) {
	t.Helper()
	backend, inferenceCalls := runningBackend(t, `{"running":[]}`)
	commit, ram := 8.0, 18.2
	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	cfg.Models[0].PeakRAMGiB = &ram
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(40, 17.9)
	server.memoryConsumers = consumers
	return server, inferenceCalls.Load
}

func TestAnthropicCapacityRefusalIsAClientErrorClaudeShowsAtOnce(t *testing.T) {
	server, inferenceCalls := refusedServer(t, func(limit int, byCommit bool) []memoryConsumer {
		if limit != refusalConsumerLimit || byCommit {
			t.Errorf("asked for %d consumers byCommit=%v, want %d by resident memory", limit, byCommit, refusalConsumerLimit)
		}
		return []memoryConsumer{{"firefox", 3}}
	})
	recorder := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`))
	if recorder.Code != http.StatusBadRequest || anthropicErrorType(t, recorder.Body.String()) != "invalid_request_error" {
		t.Fatalf("refusal: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error.Message, "Faltam 2,3 GiB") || !strings.Contains(body.Error.Message, "firefox 3,0 GiB") {
		t.Fatalf("refusal message = %q", body.Error.Message)
	}
	if calls := inferenceCalls(); calls != 0 {
		t.Fatalf("a refused request reached the upstream %d times", calls)
	}
}

func TestOpenAIRoutesKeepTheCapacityContractWithTheReadableText(t *testing.T) {
	server, inferenceCalls := refusedServer(t, func(int, bool) []memoryConsumer { return nil })
	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "insufficient_capacity" {
		t.Fatalf("refusal: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Faltam 2,3 GiB") {
		t.Fatalf("refusal body = %s", recorder.Body.String())
	}
	if calls := inferenceCalls(); calls != 0 {
		t.Fatalf("a refused request reached the upstream %d times", calls)
	}
}

func TestRefusalReadsTheProcessTableOnlyForAMemoryShortfall(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit, vram, device := 8.0, 13.5, 15.92
	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	cfg.Models[0].PeakVRAMGiB = &vram
	cfg.Models[0].DeviceVRAMGiB = &device
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(30, 24)
	server.memoryConsumers = func(int, bool) []memoryConsumer {
		t.Error("a VRAM refusal read the process table; closing applications cannot fix it")
		return nil
	}
	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("vram refusal: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestACommitRefusalRanksByReservedMemoryAndNamesTheVirtualMachine(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit := 22.91
	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(26.1, 30)
	server.memoryConsumers = func(limit int, byCommit bool) []memoryConsumer {
		if !byCommit {
			t.Error("a commit refusal ranked applications by resident memory")
		}
		return []memoryConsumer{{"vmmem", 4.01}, {"claude", 2.94}}
	}
	recorder := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("commit refusal: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Faltam 0,8 GiB", "os que mais reservam memória são vmmem (máquina virtual do Cowork/WSL) 4,0 GiB e claude 2,9 GiB", "aumente o arquivo de paginação"} {
		if !strings.Contains(body.Error.Message, want) {
			t.Errorf("commit refusal lacks %q: %s", want, body.Error.Message)
		}
	}
}
