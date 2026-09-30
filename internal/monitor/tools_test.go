package monitor

import "testing"

func TestClassifyToolRecognisesTheToolsByProgramAndLocation(t *testing.T) {
	for _, tc := range []struct {
		exe, path, want string
	}{
		{"llama-server.exe", "", toolLlamaCpp},
		{"llama-server-cuda.exe", "", toolLlamaCpp},
		// Bionic's own backend is LM Studio's, whatever the program is called.
		{"llama-server.exe", `C:\Users\me\.lmstudio\extensions\backends\llama.cpp-win-x86_64-vulkan-avx2-2.47.0\llama-server.exe`, toolLMStudio},
		{"llama-server.exe", `C:\IA\runtimes\llama.cpp\b10549\llama-server.exe`, toolLlamaCpp},
		{"Bionic.exe", "", toolLMStudio},
		{"LM Studio.exe", "", toolLMStudio},
		{"lms.exe", "", toolLMStudio},
		{"node.exe", `C:\Users\me\.lmstudio\bin\llmster\node.exe`, toolLMStudio},
		{"helper.exe", `C:\Users\me\AppData\Local\Programs\Bionic\resources\helper.exe`, toolLMStudio},
		{"ollama.exe", "", toolOllama},
		{"ollama_llama_server.exe", "", toolOllama},
		{"koboldcpp.exe", "", toolKobold},
		{"jan.exe", `C:\Users\me\AppData\Local\Programs\jan\jan.exe`, toolJan},
		{"jan.exe", `C:\Games\jan.exe`, ""},
		{"gpt4all.exe", "", toolGPT4All},
		{"chrome.exe", `C:\Program Files\Google\Chrome\Application\chrome.exe`, ""},
		{"node.exe", `C:\Program Files\Adobe\node.exe`, ""},
		{"", `C:\Users\me\AppData\Local\Programs\Ollama\ollama.exe`, toolOllama},
		{"", "", ""},
	} {
		if got := classifyTool(tc.exe, tc.path); got != tc.want {
			t.Errorf("classifyTool(%q, %q) = %q, want %q", tc.exe, tc.path, got, tc.want)
		}
	}
}

func TestToolLabelsAreHumanAndUnknownToolsKeepTheirName(t *testing.T) {
	if toolLabel(toolLMStudio) != "LM Studio / Bionic" || toolLabel("something") != "something" {
		t.Fatalf("labels: %q %q", toolLabel(toolLMStudio), toolLabel("something"))
	}
}

func TestManagedByEdgeWalksTheParentChain(t *testing.T) {
	table := map[uint32]procEntry{
		1:  {PID: 1, PPID: 0, Exe: "System"},
		10: {PID: 10, PPID: 1, Exe: "cia-supervisor.exe"},
		11: {PID: 11, PPID: 10, Exe: "llama-swap.exe"},
		12: {PID: 12, PPID: 11, Exe: "llama-server.exe"},
		20: {PID: 20, PPID: 1, Exe: "explorer.exe"},
		21: {PID: 21, PPID: 20, Exe: "llama-server.exe"},
		30: {PID: 30, PPID: 31, Exe: "a.exe"},
		31: {PID: 31, PPID: 30, Exe: "b.exe"},
	}
	if !managedByEdge(table, 12) {
		t.Error("a llama-server under llama-swap was not recognised as the edge's")
	}
	if managedByEdge(table, 21) {
		t.Error("a llama-server started from the desktop was taken for the edge's")
	}
	if managedByEdge(table, 30) {
		t.Error("a cycle in the table was taken for the edge's")
	}
	if managedByEdge(table, 999) {
		t.Error("an unknown pid was taken for the edge's")
	}
}
