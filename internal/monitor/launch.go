package monitor

import (
	"strconv"
	"strings"
)

// launchInfo is what a model server's own command line says about the model it
// was started with. A server that keeps its HTTP API behind a key of its own -
// LM Studio and Bionic start their model process that way - answers nothing to
// the monitor, but its launch arguments still name the model file and the
// window it was given.
//
// The command line is where such a tool also puts its key. So the only thing
// that ever leaves the code that reads it is this struct, built from an
// allowlist of flags; the line itself is never stored, logged, sent to the page
// or used to call the API. Reading a process's command line needs no access to
// its memory: the kernel hands it over to a caller with limited query rights.
type launchInfo struct {
	File      string
	Alias     string
	Context   *int64
	Parallel  *int64
	GPULayers *int64
	// LogFile is where the server was told to write its log. It is a path the
	// monitor opens to read timings and is never sent anywhere.
	LogFile string
}

// launchFlags maps each flag the monitor reads to the field it fills. Nothing
// else in the arguments is inspected, least of all a flag whose value is a
// secret.
var launchFlags = map[string]string{
	"-m": "file", "--model": "file",
	"-a": "alias", "--alias": "alias",
	"-c": "context", "--ctx-size": "context",
	"-np": "parallel", "--parallel": "parallel",
	"-ngl": "gpu-layers", "--n-gpu-layers": "gpu-layers", "--gpu-layers": "gpu-layers",
	"-lf": "log-file", "--log-file": "log-file",
}

// parseLaunchArguments reads the allowlisted flags of a llama.cpp-style command
// line, in either the "--flag value" or the "--flag=value" form. args[0] is the
// program and is skipped.
func parseLaunchArguments(args []string) launchInfo {
	var info launchInfo
	if len(args) < 2 {
		return info
	}
	assign := func(field, value string) {
		switch field {
		case "file":
			info.File = fileName(value)
		case "alias":
			info.Alias = strings.TrimSuffix(fileName(value), ".gguf")
		case "log-file":
			info.LogFile = strings.TrimSpace(value)
		case "context", "parallel", "gpu-layers":
			number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || number < 0 || number > 1<<40 {
				return
			}
			switch field {
			case "context":
				if number > 0 {
					info.Context = &number
				}
			case "parallel":
				if number > 0 {
					info.Parallel = &number
				}
			default:
				info.GPULayers = &number
			}
		}
	}
	for index := 1; index < len(args); index++ {
		argument := args[index]
		name, value, inline := strings.Cut(argument, "=")
		field, wanted := launchFlags[name]
		if !wanted {
			continue
		}
		if !inline {
			if index+1 >= len(args) {
				break
			}
			index++
			value = args[index]
		}
		assign(field, value)
	}
	return info
}

// model turns launch arguments into what the page shows for a model, or false
// when the line named none.
func (l launchInfo) model() (sourceModel, bool) {
	id := firstNonEmpty(l.Alias, strings.TrimSuffix(l.File, ".gguf"))
	if id == "" {
		return sourceModel{}, false
	}
	return sourceModel{
		ID: id, Quantization: quantFromName(l.File), ContextLoaded: l.Context,
		Slots: l.Parallel, GPULayers: l.GPULayers, Loaded: boolPtr(true),
	}, true
}
