package monitor

import (
	"path/filepath"
	"strings"
)

// procEntry is what the process table says about one process. The image path is
// asked for separately and only when a decision needs it, because reading it
// means opening the process.
type procEntry struct {
	PID  uint32
	PPID uint32
	Exe  string
}

// The inference tools the monitor recognises by the program that runs them.
// Recognition is what lets a GPU process be labelled and its API be probed;
// nothing about a tool is assumed beyond its image name and install location.
const (
	toolLMStudio = "lm-studio"
	toolOllama   = "ollama"
	toolLlamaCpp = "llama.cpp"
	toolKobold   = "koboldcpp"
	toolJan      = "jan"
	toolGPT4All  = "gpt4all"
)

var toolLabels = map[string]string{
	toolLMStudio: "LM Studio / Bionic",
	toolOllama:   "Ollama",
	toolLlamaCpp: "llama.cpp",
	toolKobold:   "KoboldCpp",
	toolJan:      "Jan",
	toolGPT4All:  "GPT4All",
}

func toolLabel(tool string) string {
	if label, ok := toolLabels[tool]; ok {
		return label
	}
	return tool
}

// classifyTool names the inference tool a process belongs to, or "" when it is
// none the monitor knows. Both arguments may be empty: the path needs the
// process to be openable, and an unopenable one is classified by name alone.
func classifyTool(exe, path string) string {
	name := strings.ToLower(strings.TrimSpace(exe))
	if name == "" && path != "" {
		name = strings.ToLower(filepath.Base(path))
	}
	location := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	switch {
	// LM Studio and Bionic run models through a llama.cpp server of their own,
	// kept under their install location. Where a program lives outranks what it
	// is called, or that server would be reported as a stand-alone llama.cpp.
	case name == "bionic.exe", name == "lm studio.exe", name == "lms.exe", strings.HasPrefix(name, "llmster"),
		strings.Contains(location, `\.lmstudio\`), strings.Contains(location, `\lm studio\`),
		strings.Contains(location, `\programs\bionic\`):
		return toolLMStudio
	case strings.HasPrefix(name, "llama-server"), strings.HasPrefix(name, "llama-cli"):
		return toolLlamaCpp
	case strings.HasPrefix(name, "ollama"), strings.Contains(location, `\programs\ollama\`):
		return toolOllama
	case strings.HasPrefix(name, "koboldcpp"):
		return toolKobold
	case name == "jan.exe" && strings.Contains(location, `\jan\`):
		return toolJan
	case strings.HasPrefix(name, "gpt4all"):
		return toolGPT4All
	}
	return ""
}

// edgeParents are the programs that start and supervise the edge's own model
// processes. A model process below one of them belongs to the edge, which
// already reports it in full.
var edgeParents = map[string]struct{}{
	"llama-swap.exe":     {},
	"cia-edge.exe":       {},
	"cia-supervisor.exe": {},
}

// managedByEdge reports whether pid descends from one of the edge's own
// processes. The walk is bounded so a cycle in a stale table cannot loop.
func managedByEdge(table map[uint32]procEntry, pid uint32) bool {
	current := pid
	for depth := 0; depth < 8; depth++ {
		entry, ok := table[current]
		if !ok || entry.PPID == 0 || entry.PPID == current {
			return false
		}
		parent, ok := table[entry.PPID]
		if !ok {
			return false
		}
		if _, managed := edgeParents[strings.ToLower(parent.Exe)]; managed {
			return true
		}
		current = entry.PPID
	}
	return false
}
