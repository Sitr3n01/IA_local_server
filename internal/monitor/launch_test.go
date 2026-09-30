package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A key of the kind Bionic passes on its model server's command line. It must
// never survive the parser, in any field.
const testAPIKey = "sk-test-DO-NOT-KEEP-0123456789abcdef"

func TestParseLaunchArgumentsKeepsOnlyTheAllowlistedFlags(t *testing.T) {
	info := parseLaunchArguments([]string{
		`C:\Users\me\.lmstudio\backends\llama-server.exe`,
		"--model", `C:\Users\me\.lmstudio\models\unsloth\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf`,
		"--host", "127.0.0.1", "--port", "54689",
		"--api-key", testAPIKey,
		"--ctx-size", "16384", "-np", "2", "--n-gpu-layers", "99", "--jinja",
	})
	if info.File != "gemma-4-12B-it-qat-UD-Q4_K_XL.gguf" {
		t.Fatalf("file = %q; only the file name may be kept", info.File)
	}
	if info.Context == nil || *info.Context != 16384 || info.Parallel == nil || *info.Parallel != 2 || info.GPULayers == nil || *info.GPULayers != 99 {
		t.Fatalf("info = %+v", info)
	}
	dump := info.File + info.Alias
	if strings.Contains(dump, testAPIKey) || strings.Contains(dump, "sk-test") || strings.Contains(dump, ".lmstudio") {
		t.Fatalf("something beyond the allowlist survived: %q", dump)
	}
}

func TestParseLaunchArgumentsReadsTheLogFileTheServerWasToldToWrite(t *testing.T) {
	info := parseLaunchArguments([]string{"llama-server", "-m", "a.gguf", "--log-file", `D:\logs\server.log`, "--api-key", testAPIKey})
	if info.LogFile != `D:\logs\server.log` {
		t.Fatalf("log file = %q", info.LogFile)
	}
	if got := parseLaunchArguments([]string{"llama-server", "-lf=/var/log/llama.log"}).LogFile; got != "/var/log/llama.log" {
		t.Fatalf("short form = %q", got)
	}
}

func TestParseLaunchArgumentsAcceptsBothFlagForms(t *testing.T) {
	info := parseLaunchArguments([]string{"llama-server", "-m=D:/models/big-Q6_K.gguf", "--alias=my model", "-c=4096", "--parallel=1", "-ngl=0"})
	if info.File != "big-Q6_K.gguf" || info.Alias != "my model" || info.Context == nil || *info.Context != 4096 ||
		info.Parallel == nil || *info.Parallel != 1 || info.GPULayers == nil || *info.GPULayers != 0 {
		t.Fatalf("info = %+v", info)
	}
}

func TestParseLaunchArgumentsIgnoresWhatItCannotTrust(t *testing.T) {
	for name, args := range map[string][]string{
		"no arguments":           nil,
		"only the program":       {"llama-server"},
		"a flag without a value": {"llama-server", "--model"},
		"a non-numeric window":   {"llama-server", "-m", "a.gguf", "-c", "lots"},
		"a negative window":      {"llama-server", "-m", "a.gguf", "-c", "-5"},
		"an absurd window":       {"llama-server", "-m", "a.gguf", "-c", "99999999999999999999"},
	} {
		info := parseLaunchArguments(args)
		if info.Context != nil {
			t.Errorf("%s: a bad window was kept: %+v", name, info)
		}
	}
	if _, ok := (launchInfo{}).model(); ok {
		t.Fatal("a line that named no model produced one")
	}
	model, ok := parseLaunchArguments([]string{"x", "-m", "C:/m/qwen-UD-Q3_K_XL.gguf", "-c", "8192", "-np", "3"}).model()
	if !ok || model.ID != "qwen-UD-Q3_K_XL" || model.Quantization != "Q3_K_XL" || model.ContextLoaded == nil || *model.ContextLoaded != 8192 ||
		model.Slots == nil || *model.Slots != 3 || model.Loaded == nil || !*model.Loaded {
		t.Fatalf("model = %+v ok=%v", model, ok)
	}
}

func TestDiscoveryNamesTheModelOfAProtectedServerFromItsLaunchArguments(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	started := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	host := &fakeHost{
		table:   map[uint32]procEntry{10: {PID: 10, PPID: 1, Exe: "Bionic.exe"}, 20: {PID: 20, PPID: 10, Exe: "llama-server.exe"}},
		paths:   map[uint32]string{20: `C:\Users\me\.lmstudio\extensions\backends\llama.cpp-vulkan\llama-server.exe`},
		listen:  []tcpListener{loopbackListener(portOf(t, server), 20)},
		launch:  map[uint32]launchInfo{20: parseLaunchArguments([]string{"llama-server", "--model", `C:\m\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf`, "-c", "8192", "--api-key", testAPIKey})},
		ram:     map[uint32]float64{20: 7 * 1024 * 1024 * 1024},
		started: map[uint32]time.Time{20: started},
	}
	gpu := []gpuProcess{{PID: 20, Name: "llama-server.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 7400, GPUUtil: ptr(0)}}
	sources := newTestDiscoverer(host).discover(context.Background(), gpu)

	if len(sources) != 1 {
		t.Fatalf("sources = %+v", sources)
	}
	s := sources[0]
	if s.Status != sourceProtected || s.Kind != kindLMStudio || len(s.Models) != 1 || s.Models[0].ID != "gemma-4-12B-it-qat-UD-Q4_K_XL" ||
		s.Models[0].Quantization != "Q4_K_XL" || s.Models[0].ContextLoaded == nil || *s.Models[0].ContextLoaded != 8192 {
		t.Fatalf("source = %+v", s)
	}
	if s.RAMBytes == nil || *s.RAMBytes != 7*1024*1024*1024 || s.StartedAt != "2026-09-29T12:00:00Z" {
		t.Fatalf("memory/start = %v %q", s.RAMBytes, s.StartedAt)
	}
	if !s.Stoppable || s.StopTarget != "llama-server.exe · PID 20" || s.stopPID != 20 {
		t.Fatalf("stop = %v %q %d", s.Stoppable, s.StopTarget, s.stopPID)
	}
	if authorization != "" {
		t.Fatalf("the probe sent %q as credentials", authorization)
	}
	if !strings.Contains(s.Note, "descarta o resto") || strings.Contains(s.Note, testAPIKey) {
		t.Fatalf("note = %q", s.Note)
	}
}

func TestDiscoveryNeverOffersToEndTheEdgesOwnModelProcess(t *testing.T) {
	host := &fakeHost{
		table: map[uint32]procEntry{
			5:  {PID: 5, Exe: "llama-swap.exe"},
			60: {PID: 60, PPID: 5, Exe: "llama-server.exe"},
			70: {PID: 70, Exe: "Bionic.exe"},
		},
	}
	// Both hold the GPU; only the tool's own process may be a source or a target.
	gpu := []gpuProcess{
		{PID: 60, Name: "llama-server.exe", Tool: toolLlamaCpp, VRAMDedicatedMiB: 9000},
		{PID: 70, Name: "Bionic.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 3000},
	}
	sources := newTestDiscoverer(host).discover(context.Background(), gpu)
	if len(sources) != 1 || sources[0].Kind != kindLMStudio || sources[0].stopPID != 70 {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].VRAMDedicatedMiB == nil || *sources[0].VRAMDedicatedMiB != 3000 {
		t.Fatalf("the edge's memory was added to another tool's: %v", sources[0].VRAMDedicatedMiB)
	}
}

func TestStopTargetIsTheHolderOfTheModelAndNeverASystemProcess(t *testing.T) {
	procs := map[uint32]procEntry{}
	source := inferenceSource{PID: 10, tool: toolLMStudio}
	gpu := []gpuProcess{
		{PID: 10, Name: "Bionic.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 400},
		{PID: 11, Name: "llama-server.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 7400},
		{PID: 12, Name: "firefox.exe", VRAMDedicatedMiB: 9000},
	}
	target, ok := stopTarget(source, gpu, procs, 999)
	if !ok || target.PID != 11 {
		t.Fatalf("target = %+v ok=%v; want the process holding the model", target, ok)
	}
	if _, ok := stopTarget(source, []gpuProcess{{PID: 10, Name: "Bionic.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 50}}, procs, 999); ok {
		t.Fatal("a process holding almost nothing was offered")
	}
	for _, name := range []string{"dwm.exe", "explorer.exe", "cia-edge.exe", "llama-swap.exe", "System"} {
		if endable(name, 4321, 999) {
			t.Errorf("%s was endable", name)
		}
	}
	if endable("llama-server.exe", 999, 999) || endable("llama-server.exe", 4, 999) || endable("", 4321, 999) {
		t.Error("the monitor itself, a system pid or an unnamed process was endable")
	}
}
