package forkgate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures below are a miniature fork: enough of the real shapes for every
// check to have something to decide about, and small enough that the "broken"
// variants differ from the "fixed" ones by exactly the defect under test. That
// is what makes a passing gate meaningful - each check is shown to fail on the
// specific regression it exists to catch, not merely to pass on the real tree.

const fixedPredicateHeader = `#pragma once
#include <cstdint>

enum common_cache_plan_reason { REASON_NONE = 0, REASON_COST_NOT_MINIMAL = 1, REASON_COVERAGE_INSUFFICIENT = 2, REASON_PAYLOAD_EMPTY = 3 };

struct server_cache_plan_checkpoint_evaluation {
    common_cache_plan_reason reason = REASON_COVERAGE_INSUFFICIENT;
    uint64_t lcp_tokens = 0;
    uint64_t payload_bytes = 0;
};

server_cache_plan_checkpoint_evaluation server_cache_plan_evaluate_checkpoint(
    bool payload_present, bool frontier_current, bool recurrent,
    bool checkpoint_lineage_matches, int64_t pos_min, int64_t pos_max,
    int64_t next_position, int64_t min_position_threshold,
    uint64_t payload_bytes) noexcept;

constexpr bool server_cache_plan_viable(common_cache_plan_reason reason) noexcept {
    return reason == REASON_COST_NOT_MINIMAL;
}
`

// fixedPredicate treats a recurrent or hybrid model on its own terms: the
// recurrent frontier decides, and pos_min plays no part.
const fixedPredicate = `#include "server-cache-plan-authority.h"

server_cache_plan_checkpoint_evaluation server_cache_plan_evaluate_checkpoint(
        bool payload_present, bool frontier_current, bool recurrent,
        bool checkpoint_lineage_matches, int64_t pos_min, int64_t pos_max,
        int64_t next_position, int64_t min_position_threshold,
        uint64_t payload_bytes) noexcept {
    server_cache_plan_checkpoint_evaluation out;
    out.payload_bytes = payload_bytes;
    out.reason = !payload_present ? REASON_PAYLOAD_EMPTY :
                 !frontier_current ? REASON_COVERAGE_INSUFFICIENT :
                 !checkpoint_lineage_matches ? REASON_COVERAGE_INSUFFICIENT :
                 recurrent
                    ? (pos_max < next_position ? REASON_COST_NOT_MINIMAL : REASON_COVERAGE_INSUFFICIENT)
                    : (pos_max <= next_position &&
                       (pos_min < min_position_threshold || pos_min == 0)
                        ? REASON_COST_NOT_MINIMAL : REASON_COVERAGE_INSUFFICIENT);
    return out;
}
`

// brokenPredicate is upstream's shape: one comparison for every architecture,
// resting on a pos_min that a recurrent model cannot satisfy.
const brokenPredicate = `#include "server-cache-plan-authority.h"

server_cache_plan_checkpoint_evaluation server_cache_plan_evaluate_checkpoint(
        bool payload_present, bool frontier_current, bool recurrent,
        bool checkpoint_lineage_matches, int64_t pos_min, int64_t pos_max,
        int64_t next_position, int64_t min_position_threshold,
        uint64_t payload_bytes) noexcept {
    server_cache_plan_checkpoint_evaluation out;
    out.payload_bytes = payload_bytes;
    (void) recurrent;
    (void) checkpoint_lineage_matches;
    out.reason = !payload_present ? REASON_PAYLOAD_EMPTY :
                 !frontier_current ? REASON_COVERAGE_INSUFFICIENT :
                 (pos_max <= next_position &&
                  (pos_min < min_position_threshold || pos_min == 0))
                    ? REASON_COST_NOT_MINIMAL : REASON_COVERAGE_INSUFFICIENT;
    return out;
}
`

const fixedServerContext = `
void release() {
    // Upstream erases these with a pos_min > prompt_end test.
    const int64_t prompt_end = task->n_tokens();
    const int64_t retained_end = prompt.n_tokens();
    for (auto it = prompt.checkpoints.begin(); it != prompt.checkpoints.end();) {
        const bool is_generation_checkpoint = it->n_tokens > prompt_end;
        const bool is_outside_retained = it->n_tokens > retained_end;
        if (is_generation_checkpoint && is_outside_retained) {
            it = checkpoint_drop(it, std::next(it));
        } else {
            ++it;
        }
    }
}

void update_slots() {
    const bool recurrent =
        llama_model_is_recurrent(model_tgt) ||
        llama_model_is_hybrid(model_tgt);
    const int checkpoint_min_tokens = (llama_model_is_recurrent(model_tgt) || llama_model_is_hybrid(model_tgt)) ? 4 : 64;
    do_checkpoint = do_checkpoint && (pos_min >= 0 && slot.prompt.n_tokens() >= checkpoint_min_tokens);
    const bool checkpoint_exact_frontier =
        llama_model_is_recurrent(model_tgt) || llama_model_is_hybrid(model_tgt);
    const llama_pos ckpt_pos_min = checkpoint_exact_frontier ? pos_max : pos_min;
}
`

const fixedCommonHeader = `
struct common_params {
    bool    cache_idle_slots    = true;
    int32_t n_ctx_checkpoints   = 32;
    int32_t checkpoint_min_step = 8192;
    int32_t cache_ram_mib       = 8192;
};
`

const fixedArgs = `
void common_params_parser_init() {
    add_opt(common_arg({"-ctk", "--cache-type-k"}, "TYPE", "K type",
        [](common_params & params, const std::string & value) {
            params.vbr_cache_type_k = common_vbr_is_alias(value);
            params.cache_type_k = kv_cache_type_from_str(value);
        }));
    add_opt(common_arg({"-ctv", "--cache-type-v"}, "TYPE", "V type",
        [](common_params & params, const std::string & value) {
            params.vbr_cache_type_v = common_vbr_is_alias(value);
            params.cache_type_v = kv_cache_type_from_str(value);
        }));
}
`

func fixtureSources(t *testing.T, mutate func(map[string]string)) sourceSet {
	t.Helper()
	files := map[string]string{
		"tools/server/server-context.cpp":                fixedServerContext,
		"tools/server/server-cache-plan-authority.cpp":   fixedPredicate,
		"common/common.h":                                fixedCommonHeader,
		"common/arg.cpp":                                 fixedArgs + allQualificationFlagDeclarations(),
		"tools/server/server-cache-plan-authority.h.txt": fixedPredicateHeader,
	}
	if mutate != nil {
		mutate(files)
	}
	stripped := make(map[string]string, len(files))
	for name, body := range files {
		stripped[name] = stripComments(body)
	}
	return sourceSet{root: t.TempDir(), files: stripped}
}

func allQualificationFlagDeclarations() string {
	var builder strings.Builder
	for _, flag := range qualificationFlags {
		builder.WriteString("    add_opt(common_arg({\"" + flag + "\"}, \"\", \"\"));\n")
	}
	return builder.String()
}

func TestStructuralChecksAcceptTheQualifiedShape(t *testing.T) {
	sources := fixtureSources(t, nil)
	facts := map[string]string{}
	for _, check := range []Check{
		smallPromptCheck(sources),
		frontierCaptureCheck(sources),
		generationRetentionCheck(sources),
		requiredFlagsCheck(sources),
		controlVariableCheck(sources, facts),
	} {
		if !check.Passed {
			t.Errorf("%s rejected a qualified fork: %s", check.ID, check.Detail)
		}
	}
	for _, symbol := range []string{"cache_ram_mib", "cache_idle_slots", "n_ctx_checkpoints", "checkpoint_min_step"} {
		if facts[symbol] == "" {
			t.Errorf("the shipped default for %s was not recorded, so a profile cannot be required to pin it", symbol)
		}
	}
	if facts["cache_ram_mib"] == "0" {
		t.Fatal("the fixture no longer reproduces a non-zero host prompt cache default, which is the reason the profile must pin it")
	}
}

// Each case restores one piece of upstream behaviour. A gate that still passes
// here would pass on a build that reprocesses the whole context every turn.
func TestStructuralChecksRejectUpstreamBehaviour(t *testing.T) {
	tests := map[string]struct {
		mutate func(map[string]string)
		check  func(sourceSet) Check
		want   string
	}{
		"transformer-sized checkpoint floor": {
			mutate: func(files map[string]string) {
				files["tools/server/server-context.cpp"] = strings.Replace(
					files["tools/server/server-context.cpp"],
					"? 4 : 64", "? 64 : 64", 1)
			},
			check: smallPromptCheck,
			want:  "not below the transformer floor",
		},
		"checkpoint floor no longer distinguishes hybrids": {
			mutate: func(files map[string]string) {
				files["tools/server/server-context.cpp"] = strings.Replace(
					files["tools/server/server-context.cpp"],
					"const int checkpoint_min_tokens = (llama_model_is_recurrent(model_tgt) || llama_model_is_hybrid(model_tgt)) ? 4 : 64;",
					"const int checkpoint_min_tokens = 64;", 1)
			},
			check: smallPromptCheck,
			want:  "does not distinguish recurrent or hybrid models",
		},
		"capture records the range minimum": {
			mutate: func(files map[string]string) {
				files["tools/server/server-context.cpp"] = strings.Replace(
					files["tools/server/server-context.cpp"],
					"ckpt_pos_min = checkpoint_exact_frontier ? pos_max : pos_min;",
					"ckpt_pos_min = pos_min;", 1)
			},
			check: frontierCaptureCheck,
			want:  "record pos_max as the checkpoint position",
		},
		"release erases recurrent generation checkpoints": {
			mutate: func(files map[string]string) {
				files["tools/server/server-context.cpp"] = strings.Replace(
					files["tools/server/server-context.cpp"],
					"const bool is_generation_checkpoint = it->n_tokens > prompt_end;",
					"const bool is_generation_checkpoint = it->pos_min > prompt_end;", 1)
			},
			check: generationRetentionCheck,
			want:  "comparing pos_min against the prompt end",
		},
		"a profile option was removed": {
			mutate: func(files map[string]string) {
				files["common/arg.cpp"] = strings.Replace(
					files["common/arg.cpp"], `"--checkpoint-min-step"`, `"--checkpoint-every-n-tokens"`, 1)
			},
			check: requiredFlagsCheck,
			want:  "--checkpoint-min-step",
		},
		"an explicit KV type no longer clears the fork's own cache format": {
			mutate: func(files map[string]string) {
				files["common/arg.cpp"] = strings.ReplaceAll(
					files["common/arg.cpp"], "common_vbr_is_alias(value)", "true")
			},
			check: func(sources sourceSet) Check { return controlVariableCheck(sources, map[string]string{}) },
			want:  "clears the variable-bitrate KV alias",
		},
		"a shipped default became unreadable": {
			mutate: func(files map[string]string) {
				files["common/common.h"] = strings.Replace(
					files["common/common.h"], "int32_t cache_ram_mib       = 8192;", "", 1)
			},
			check: func(sources sourceSet) Check { return controlVariableCheck(sources, map[string]string{}) },
			want:  "cache_ram_mib",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			check := test.check(fixtureSources(t, test.mutate))
			if check.Passed {
				t.Fatalf("%s accepted upstream behaviour", check.ID)
			}
			if !strings.Contains(check.Detail, test.want) {
				t.Fatalf("%s failed for the wrong reason: %s", check.ID, check.Detail)
			}
		})
	}
}

// The probe is the primary evidence, so it gets the same treatment: it must
// accept the corrected predicate and reject the upstream one, by compiling and
// running them rather than by reading them.
func TestProbeDistinguishesTheCorrectedPredicate(t *testing.T) {
	compiler := findCompiler()
	if compiler == "" {
		t.Skip("no C++ driver on PATH; the probe is exercised in CI where one is present")
	}

	for name, predicate := range map[string]struct {
		body       string
		wantPassed bool
		wantDetail string
	}{
		"corrected": {body: fixedPredicate, wantPassed: true},
		"upstream":  {body: brokenPredicate, wantPassed: false, wantDetail: "recurrent-ignores-pos-min"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFixtureTree(t, root, map[string]string{
				"tools/server/server-cache-plan-authority.h":   fixedPredicateHeader,
				"tools/server/server-cache-plan-authority.cpp": predicate.body,
			})
			check := probeCheck(Options{Source: root, Compiler: compiler, WorkDir: t.TempDir()}, nil)
			if check.Passed != predicate.wantPassed {
				t.Fatalf("probe verdict %v, want %v: %s", check.Passed, predicate.wantPassed, check.Detail)
			}
			if predicate.wantDetail != "" && !strings.Contains(check.Detail, predicate.wantDetail) {
				t.Fatalf("probe failed for the wrong reason: %s", check.Detail)
			}
		})
	}
}

func TestProbeFailsWhenThePredicateCannotBeFound(t *testing.T) {
	root := t.TempDir()
	writeFixtureTree(t, root, map[string]string{"tools/server/server-context.cpp": fixedServerContext})
	check := probeCheck(Options{Source: root, Compiler: "g++", WorkDir: t.TempDir()}, nil)
	if check.Passed {
		t.Fatal("the probe passed without a predicate to interrogate")
	}
	if !strings.Contains(check.Detail, "renamed or removed") {
		t.Fatalf("a missing predicate was not reported as such: %s", check.Detail)
	}
}

func TestProbeWithoutACompilerIsNeverAPass(t *testing.T) {
	check := probeCheck(Options{Source: t.TempDir()}, nil)
	if check.Passed {
		t.Fatal("the gate inferred the fix from source shape alone")
	}
}

func TestRevisionCheckRefusesMovingReferences(t *testing.T) {
	for _, revision := range []string{"", "master", "main", "latest", "HEAD", "799e3995", "799E3995CD4F19AA9F6A3FA9FB5B4674422BF0EE"} {
		check := revisionCheck(Options{Source: t.TempDir(), Revision: revision})
		if check.Passed {
			t.Fatalf("revision %q was accepted as a runtime pin", revision)
		}
	}
}

func TestRunRefusesWithoutACheckout(t *testing.T) {
	if _, err := Run(Options{Revision: strings.Repeat("a", 40)}); err == nil {
		t.Fatal("the gate reached a verdict without a checkout")
	}
}

func TestStripCommentsKeepsCodeAndLiterals(t *testing.T) {
	source := "int a = 1; // pos_min > prompt_end\n/* pos_min > prompt_end */\nconst char * s = \"// not a comment\";\n"
	stripped := stripComments(source)
	if strings.Contains(stripped, "prompt_end") {
		t.Fatalf("comment text survived stripping: %q", stripped)
	}
	if !strings.Contains(stripped, `"// not a comment"`) {
		t.Fatalf("a string literal was damaged: %q", stripped)
	}
	if got, want := strings.Count(stripped, "\n"), strings.Count(source, "\n"); got != want {
		t.Fatalf("line count changed from %d to %d", want, got)
	}
}

func writeFixtureTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := mkdirAll(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(path, body); err != nil {
			t.Fatal(err)
		}
	}
}

func findCompiler() string {
	for _, candidate := range []string{"g++", "clang++", "c++"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func mkdirAll(path string) error { return os.MkdirAll(path, 0o755) }

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }
