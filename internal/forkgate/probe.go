package forkgate

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// probeSource interrogates the fork's checkpoint-selection predicate directly.
//
// The predicate is a pure function of scalars, which is what makes this
// possible: the probe links one translation unit out of the pinned tree and
// calls it, with no model, no memory backend, and no server. Unrelated symbols
// in that translation unit are deliberately left unresolved - the probe never
// reaches them, and pulling in their dependencies would mean building llama.cpp
// to answer a question about forty lines of arithmetic.
//
// What is asserted is an invariant, not a value. `pos_min` and the position
// threshold are the transformer-only comparison that ggml-org/llama.cpp#22384
// identifies as unsatisfiable on a recurrent model, where `pos_min` tracks the
// full sequence frontier. So the recurrent verdict is swept across the whole
// plausible range of both and must never move; the recurrent frontier is swept
// and must decide the verdict on its own. A fork that restored the upstream
// comparison fails the first sweep, and a fork that ignores position entirely -
// which would restore garbage state - fails the second.
const probeSource = `#include "server-cache-plan-authority.h"

#include <cstdio>

static bool viable(bool recurrent, long long pos_min, long long pos_max,
                   long long next_position, long long threshold) {
    const auto evaluation = server_cache_plan_evaluate_checkpoint(
        /* payload_present            */ true,
        /* frontier_current           */ true,
        recurrent,
        /* checkpoint_lineage_matches */ true,
        pos_min, pos_max, next_position, threshold,
        /* payload_bytes              */ 4096);
    return server_cache_plan_viable(evaluation.reason);
}

static void emit(const char * id, bool passed) {
    std::printf("check %s %d\n", id, passed ? 1 : 0);
}

int main() {
    // A checkpoint captured 2000 tokens behind the position the next turn
    // resumes from: the ordinary agentic shape, one tool result later.
    const long long frontier = 60000;
    const long long next     = 62000;

    // On a recurrent or hybrid model pos_min carries no usable information -
    // it follows the sequence frontier - so the verdict must be invariant
    // under it and under the threshold derived from it.
    const long long positions[]  = {0, 1, 30000, 59999, 60000, 60001, 62000, 70000};
    const long long thresholds[] = {0, 1, 30000, 59999, 60000, 61999, 62000, 70000};
    const bool expected = viable(true, frontier, frontier, next, next);
    bool invariant = expected;
    for (long long pos_min : positions) {
        for (long long threshold : thresholds) {
            if (viable(true, pos_min, frontier, next, threshold) != expected) {
                invariant = false;
            }
        }
    }
    emit("recurrent-ignores-pos-min", invariant);

    // The recurrent state position is what decides instead: a checkpoint behind
    // the resume frontier is selectable, one at or beyond it is not, because
    // restoring it would install state for tokens that were never processed.
    const bool frontier_decides =
        viable(true, frontier, frontier, next, next) &&
        !viable(true, next,     next,     next, next) &&
        !viable(true, next + 1, next + 1, next, next);
    emit("recurrent-uses-frontier", frontier_decides);

    // Normal transformers must keep the upstream comparison exactly. If this
    // fails the fork changed behaviour for every non-hybrid model, which is a
    // regression against the baseline runtime rather than a fix.
    const bool transformer_pos_min_sensitive =
        viable(false, 0,     frontier, next, next) &&
        !viable(false, next, frontier, next, next);
    const bool transformer_threshold_sensitive =
        viable(false, 30000, frontier, next, next) &&
        !viable(false, 30000, frontier, next, 30000);
    emit("transformer-semantics-preserved",
         transformer_pos_min_sensitive && transformer_threshold_sensitive);

    return 0;
}
`

// probeExpectations is every check the probe is required to emit. A probe that
// prints fewer lines than this - because the predicate was renamed, or the
// translation unit moved - fails the gate rather than passing on silence.
var probeExpectations = []string{
	"recurrent-ignores-pos-min",
	"recurrent-uses-frontier",
	"transformer-semantics-preserved",
}

var probeIncludes = []string{
	"tools/server",
	"common",
	"src",
	"vendor",
	"ggml/include",
	"include",
}

// defaultLinkArgs leaves symbols the probe never calls unresolved. The two
// spellings cover the GNU and MSVC linkers; any other toolchain has to be told
// explicitly, which is preferable to guessing and reporting a link failure as a
// property of the fork.
func defaultLinkArgs(compiler string) []string {
	name := strings.ToLower(filepath.Base(compiler))
	name = strings.TrimSuffix(name, ".exe")
	if name == "cl" || name == "clang-cl" {
		return []string{"/link", "/FORCE:UNRESOLVED"}
	}
	return []string{"-Wl,--unresolved-symbols=ignore-all"}
}

func probeCheck(options Options, report *Report) Check {
	check := Check{
		ID:    "checkpoint-predicate-recurrent-semantics",
		Kind:  KindExecutable,
		Title: "the checkpoint predicate selects recurrent checkpoints by frontier, not by pos_min",
	}
	if strings.TrimSpace(options.Compiler) == "" {
		check.Detail = "no C++ compiler was supplied, so the predicate was never executed; the gate refuses to infer the fix from source shape alone"
		return check
	}

	unit, err := probeTranslationUnit(options.Source)
	if err != nil {
		check.Detail = err.Error()
		return check
	}

	workDir := options.WorkDir
	if workDir == "" {
		created, err := os.MkdirTemp("", "cia-fork-gate-")
		if err != nil {
			check.Detail = fmt.Sprintf("could not create a probe working directory: %v", err)
			return check
		}
		defer os.RemoveAll(created)
		workDir = created
	}

	sourcePath := filepath.Join(workDir, "checkpoint-probe.cpp")
	if err := os.WriteFile(sourcePath, []byte(probeSource), 0o600); err != nil {
		check.Detail = fmt.Sprintf("could not write the probe: %v", err)
		return check
	}
	binaryPath := filepath.Join(workDir, "checkpoint-probe")

	arguments := []string{"-std=c++17"}
	for _, include := range probeIncludes {
		arguments = append(arguments, "-I", filepath.Join(options.Source, filepath.FromSlash(include)))
	}
	arguments = append(arguments, "-o", binaryPath, sourcePath, unit)
	linkArgs := options.LinkArgs
	if linkArgs == nil {
		linkArgs = defaultLinkArgs(options.Compiler)
	}
	arguments = append(arguments, linkArgs...)

	build := exec.Command(options.Compiler, arguments...)
	if output, err := build.CombinedOutput(); err != nil {
		check.Detail = fmt.Sprintf("the predicate in %s could not be compiled in isolation, so its behaviour is unknown: %v: %s",
			filepath.Base(unit), err, truncate(string(output), 800))
		return check
	}

	output, err := exec.Command(binaryPath).CombinedOutput()
	if err != nil {
		check.Detail = fmt.Sprintf("the probe did not run to completion: %v: %s", err, truncate(string(output), 400))
		return check
	}

	results, err := parseProbeOutput(string(output))
	if err != nil {
		check.Detail = err.Error()
		return check
	}

	failed := make([]string, 0, len(probeExpectations))
	for _, id := range probeExpectations {
		passed, reported := results[id]
		if !reported {
			failed = append(failed, id+" (not reported)")
			continue
		}
		if !passed {
			failed = append(failed, id)
		}
	}
	if len(failed) > 0 {
		check.Detail = fmt.Sprintf("the pinned commit does not implement the correction discussed in %s: %s",
			CheckpointFixReference, strings.Join(failed, ", "))
		return check
	}
	if report != nil {
		report.Facts["checkpoint_predicate_unit"] = mustRelative(options.Source, unit)
	}
	check.Passed = true
	check.Detail = fmt.Sprintf("the compiled predicate ignores pos_min and the position threshold for recurrent and hybrid models, decides on the recurrent frontier, and leaves transformer selection unchanged (%s)",
		strings.Join(probeExpectations, ", "))
	return check
}

var predicateDefinition = regexp.MustCompile(`(?m)^\s*server_cache_plan_checkpoint_evaluation\s+server_cache_plan_evaluate_checkpoint\s*\(`)

// probeTranslationUnit finds the file that *defines* the predicate rather than
// assuming a path. The fork moves this code between planner translation units
// as its cache authority evolves, and a hardcoded filename would report that
// reorganisation as a missing fix.
func probeTranslationUnit(root string) (string, error) {
	var candidates []string
	for _, directory := range []string{"tools/server", "common", "src"} {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(directory), "*.cpp"))
		if err != nil {
			return "", err
		}
		candidates = append(candidates, matches...)
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if predicateDefinition.Match(data) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no translation unit defines server_cache_plan_evaluate_checkpoint; the checkpoint predicate was renamed or removed, so its behaviour has to be re-established by hand before this commit can be adopted")
}

var probeLine = regexp.MustCompile(`^check ([a-z0-9-]+) ([01])$`)

func parseProbeOutput(output string) (map[string]bool, error) {
	results := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		match := probeLine.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if match == nil {
			continue
		}
		results[match[1]] = match[2] == "1"
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("the probe produced no verdicts: %s", truncate(output, 400))
	}
	return results, nil
}

func gitHead(root string) (string, error) {
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func mustRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(relative)
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
