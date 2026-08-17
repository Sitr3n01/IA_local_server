// Package forkgate decides whether a pinned buun-llama-cpp commit may be built
// and adopted as the Qwen3.8 agentic runtime.
//
// The question it answers is narrow and deliberately hostile: does *this exact
// commit* implement the hybrid/recurrent context-checkpoint correction discussed
// in ggml-org/llama.cpp#22384, or only appear to? The presence of a patch, a
// commit subject, or a flag in `--help` is not evidence — llama.cpp accepts
// `--ctx-checkpoints` on builds where checkpoints are created and immediately
// discarded, which is exactly the defect being escaped.
//
// So the primary check is executable. The fork's own checkpoint-selection
// predicate is compiled out of the pinned tree and interrogated directly, and
// what is asserted is a *semantic invariant* rather than a line, a symbol
// spelling, or a diff: for a recurrent or hybrid model the verdict must not vary
// with `pos_min` or the position threshold at all, and must vary with the
// recurrent frontier instead. A refactor that preserves the fix keeps passing; a
// refactor that quietly restores the transformer-only comparison fails, and so
// does a fork that never had the fix.
//
// Everything here reads the source tree and runs a compiler. Nothing loads a
// model, opens a port, or reaches the network.
package forkgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CheckpointFixReference is the upstream discussion this gate holds the fork to.
const CheckpointFixReference = "https://github.com/ggml-org/llama.cpp/pull/22384"

// SchemaVersion versions the report shape written to disk. The report is the
// artifact the manifest's provenance.checkpoint_fix.gate_report_sha256 commits
// to, so its shape is part of the contract.
const SchemaVersion = 1

// Kind separates what a check actually proves. The distinction matters when a
// check fails: an executable failure is a statement about behaviour, a
// structural failure may equally mean the fork was reorganised.
type Kind string

const (
	// KindExecutable compiles fork code and observes what it computes.
	KindExecutable Kind = "executable"
	// KindStructural reads the fork source for a construct, located by name
	// rather than by line number, and reasons about the expression it finds.
	KindStructural Kind = "structural"
	// KindIdentity compares the checkout against the requested pin.
	KindIdentity Kind = "identity"
)

// Check is one decision. Detail is written for an operator reading a failed
// gate, so it states what was looked for and what was found.
type Check struct {
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	Title  string `json:"title"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// Report is the whole verdict. It carries no paths from the operator's machine
// beyond the ones needed to reproduce the run, and no model or prompt content:
// there is none to carry, because nothing here runs inference.
type Report struct {
	SchemaVersion int    `json:"schema_version"`
	Repository    string `json:"repository"`
	Revision      string `json:"revision"`
	Reference     string `json:"checkpoint_fix_reference"`
	GeneratedUTC  string `json:"generated_utc"`
	Compiler      string `json:"compiler,omitempty"`
	// Facts are fork defaults that differ from an upstream release build and
	// therefore have to be pinned explicitly by any profile using this runtime.
	// They are reported rather than judged: the manifest is where they are
	// pinned, and Common.ps1 is what refuses a profile that leaves them open.
	Facts    map[string]string `json:"observed_defaults,omitempty"`
	Checks   []Check           `json:"checks"`
	Passed   bool              `json:"passed"`
	Evidence string            `json:"evidence,omitempty"`
}

// Options configures one gate run.
type Options struct {
	// Source is a checkout of the fork at Revision.
	Source string
	// Revision is the full 40-hex commit being qualified. A branch name, a tag,
	// an abbreviation, or HEAD is refused: the runtime's identity is the commit.
	Revision string
	// Repository is the source repository URL recorded in the report.
	Repository string
	// Compiler is the C++ driver used for the executable probe. Empty disables
	// the probe, which makes the gate fail: the probe is the primary evidence
	// and there is no configuration in which its absence is a pass.
	Compiler string
	// LinkArgs overrides the linker arguments used to build the probe. The
	// probe calls one pure scalar function, so unrelated symbols in the same
	// translation unit are left unresolved on purpose.
	LinkArgs []string
	// WorkDir holds probe intermediates. A temporary directory is used when empty.
	WorkDir string
	// Now is injectable for tests.
	Now func() time.Time
}

var fullRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Run executes every check and returns a report. A non-nil error means the gate
// could not reach a verdict; a report with Passed false means it reached one and
// the answer is no. Both outcomes block adoption, and they are distinguished
// because only the second one is a statement about the fork.
func Run(options Options) (Report, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		Repository:    strings.TrimSpace(options.Repository),
		Revision:      strings.TrimSpace(options.Revision),
		Reference:     CheckpointFixReference,
		GeneratedUTC:  now().UTC().Format("2006-01-02T15:04:05Z"),
		Compiler:      options.Compiler,
		Facts:         map[string]string{},
	}
	if strings.TrimSpace(options.Source) == "" {
		return report, errors.New("a checkout of the fork is required")
	}

	report.Checks = append(report.Checks, revisionCheck(options))

	sources, err := loadSources(options.Source)
	if err != nil {
		return report, err
	}

	report.Checks = append(report.Checks,
		probeCheck(options, &report),
		smallPromptCheck(sources),
		frontierCaptureCheck(sources),
		generationRetentionCheck(sources),
		requiredFlagsCheck(sources),
		controlVariableCheck(sources, report.Facts),
	)

	report.Passed = true
	for _, check := range report.Checks {
		if !check.Passed {
			report.Passed = false
		}
	}
	if report.Passed {
		// The fork does not carry the upstream patch verbatim - it reorganised
		// the predicate into its own planner - so the strongest honest claim is
		// equivalence of behaviour, which is what was measured.
		report.Evidence = "semantic-equivalent"
	}
	return report, nil
}

// Encode renders the report exactly as it is hashed into the manifest.
func (r Report) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func revisionCheck(options Options) Check {
	check := Check{
		ID:    "revision-pinned",
		Kind:  KindIdentity,
		Title: "the runtime is pinned to a full commit, not a moving reference",
	}
	revision := strings.TrimSpace(options.Revision)
	if !fullRevision.MatchString(revision) {
		check.Detail = fmt.Sprintf("revision %q is not a full lowercase 40-hex commit; master, main, latest, HEAD and abbreviations are refused", revision)
		return check
	}
	head, err := gitHead(options.Source)
	if err != nil {
		check.Detail = fmt.Sprintf("could not read the checkout's HEAD: %v", err)
		return check
	}
	if head != revision {
		check.Detail = fmt.Sprintf("checkout is at %s but %s was requested", head, revision)
		return check
	}
	check.Passed = true
	check.Detail = "checkout HEAD matches the requested commit"
	return check
}

// sourceSet holds the fork files the checks read, with comments stripped.
// Files are keyed by their repository-relative path so a check can say which
// file disappeared.
//
// Comments are removed because this fork documents the upstream defect in prose
// next to the code that fixes it - "the upstream `pos_min > prompt_end` test
// would erase every recurrent generation checkpoint" sits directly above the
// replacement. A structural check that reads comments would find the defect it
// is looking for in the explanation of why it is gone.
type sourceSet struct {
	root  string
	files map[string]string
}

var gateSources = []string{
	"tools/server/server-context.cpp",
	"tools/server/server-cache-plan-authority.cpp",
	"common/arg.cpp",
	"common/common.h",
}

func loadSources(root string) (sourceSet, error) {
	set := sourceSet{root: root, files: map[string]string{}}
	for _, relative := range gateSources {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return set, fmt.Errorf("read %s: %w", relative, err)
		}
		set.files[relative] = stripComments(string(data))
	}
	return set, nil
}

// stripComments removes // and /* */ comments, preserving string and character
// literals and the original line count so a reported excerpt still lines up
// with the file an operator opens.
func stripComments(source string) string {
	var out strings.Builder
	out.Grow(len(source))
	const (
		code = iota
		lineComment
		blockComment
		stringLiteral
		charLiteral
	)
	state := code
	for index := 0; index < len(source); index++ {
		current := source[index]
		next := byte(0)
		if index+1 < len(source) {
			next = source[index+1]
		}
		switch state {
		case code:
			switch {
			case current == '/' && next == '/':
				state = lineComment
				index++
			case current == '/' && next == '*':
				state = blockComment
				index++
			case current == '"':
				state = stringLiteral
				out.WriteByte(current)
			case current == '\'':
				state = charLiteral
				out.WriteByte(current)
			default:
				out.WriteByte(current)
			}
		case lineComment:
			if current == '\n' {
				state = code
				out.WriteByte(current)
			}
		case blockComment:
			if current == '*' && next == '/' {
				state = code
				index++
			} else if current == '\n' {
				out.WriteByte(current)
			}
		case stringLiteral, charLiteral:
			out.WriteByte(current)
			if current == '\\' && next != 0 {
				out.WriteByte(next)
				index++
				continue
			}
			if (state == stringLiteral && current == '"') || (state == charLiteral && current == '\'') {
				state = code
			}
		}
	}
	return out.String()
}

func (s sourceSet) file(name string) string { return s.files[name] }
