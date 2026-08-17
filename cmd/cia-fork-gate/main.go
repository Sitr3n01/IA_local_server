// Command cia-fork-gate decides whether a pinned buun-llama-cpp commit may be
// built and adopted as the Qwen3.8 agentic runtime.
//
// It reads a checkout and runs a compiler. It never loads a model, opens a
// port, or reaches the network, so it is safe to run in CI and on the serving
// machine alike. A zero exit status means the commit implements the
// hybrid/recurrent context-checkpoint correction; anything else means it does
// not, or that the question could not be answered - and both block adoption.
//
//	cia-fork-gate --source <checkout> --revision <40-hex commit> \
//	              --repository https://github.com/spiritbuun/buun-llama-cpp \
//	              --compiler g++ --report gate-report.json
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sitr3n/local-ai-provider/internal/forkgate"
)

func main() {
	source := flag.String("source", "", "path to a checkout of the fork at --revision")
	revision := flag.String("revision", "", "full 40-hex commit being qualified")
	repository := flag.String("repository", "https://github.com/spiritbuun/buun-llama-cpp", "source repository URL recorded in the report")
	compiler := flag.String("compiler", "", "C++ driver used to execute the checkpoint predicate; without it the gate fails")
	report := flag.String("report", "", "write the JSON report here; its SHA-256 is what the manifest records")
	flag.Parse()

	result, err := forkgate.Run(forkgate.Options{
		Source:     *source,
		Revision:   *revision,
		Repository: *repository,
		Compiler:   *compiler,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cia-fork-gate: %v\n", err)
		os.Exit(2)
	}

	encoded, err := result.Encode()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cia-fork-gate: encode report: %v\n", err)
		os.Exit(2)
	}
	if *report != "" {
		if err := os.WriteFile(*report, encoded, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "cia-fork-gate: write report: %v\n", err)
			os.Exit(2)
		}
	}
	os.Stdout.Write(encoded)

	if !result.Passed {
		for _, check := range result.Checks {
			if !check.Passed {
				fmt.Fprintf(os.Stderr, "cia-fork-gate: %s: %s\n", check.ID, check.Detail)
			}
		}
		fmt.Fprintf(os.Stderr, "cia-fork-gate: commit %s is not eligible; do not patch it locally, record the reason and pick another commit\n", *revision)
		os.Exit(1)
	}
}
