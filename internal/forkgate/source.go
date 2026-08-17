package forkgate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The checks in this file read the fork's source. They are secondary to the
// compiled probe and are located by construct name rather than by line number,
// because the fork rewrites the surrounding code freely. Each one states what it
// looked for, so a failure caused by a rename reads as a rename rather than as
// a missing fix.

var recurrentPredicate = regexp.MustCompile(`llama_model_is_recurrent\s*\([^)]*\)\s*\|\|\s*llama_model_is_hybrid\s*\([^)]*\)`)

// smallPromptCheck confirms short prompts are still checkpointed on a hybrid
// model. Upstream refuses to capture below a fixed token count sized for a
// transformer; on a recurrent model that threshold silently removes the first
// checkpoint of a session, and an agentic loop that starts small then grows
// never gets one at all. The fork lowers it for recurrent and hybrid models, and
// what is asserted here is the relationship - recurrent threshold strictly below
// the transformer threshold - not either number.
func smallPromptCheck(sources sourceSet) Check {
	check := Check{
		ID:    "checkpoint-small-prompt-capture",
		Kind:  KindStructural,
		Title: "short prompts are checkpointed on recurrent and hybrid models",
	}
	body := sources.file("tools/server/server-context.cpp")
	assignment := findInitializer(body, "checkpoint_min_tokens")
	if assignment == "" {
		check.Detail = "no initializer for checkpoint_min_tokens was found in tools/server/server-context.cpp"
		return check
	}
	if !recurrentPredicate.MatchString(assignment) {
		check.Detail = fmt.Sprintf("the checkpoint size floor does not distinguish recurrent or hybrid models: %s", truncate(assignment, 200))
		return check
	}
	recurrentValue, transformerValue, ok := ternaryIntegers(assignment)
	if !ok {
		check.Detail = fmt.Sprintf("the checkpoint size floor is not a plain conditional between two token counts, so its effect on short prompts cannot be established: %s", truncate(assignment, 200))
		return check
	}
	if recurrentValue >= transformerValue {
		check.Detail = fmt.Sprintf("the recurrent floor (%d tokens) is not below the transformer floor (%d tokens), so short prompts are not checkpointed any sooner than upstream", recurrentValue, transformerValue)
		return check
	}
	check.Passed = true
	check.Detail = fmt.Sprintf("recurrent and hybrid models checkpoint from %d tokens against %d for transformers", recurrentValue, transformerValue)
	return check
}

// frontierCaptureCheck confirms a recurrent checkpoint records the position its
// state is actually valid at. A recurrent capture is a point-in-time state, not
// a range: recording the range minimum makes a later restore install the state
// at the wrong position, which is worse than not restoring at all because the
// continuation is silently wrong rather than merely slow.
func frontierCaptureCheck(sources sourceSet) Check {
	check := Check{
		ID:    "checkpoint-recurrent-frontier-capture",
		Kind:  KindStructural,
		Title: "a recurrent checkpoint records the frontier position of its state",
	}
	body := sources.file("tools/server/server-context.cpp")
	guards := recurrentGuardNames(body)
	if len(guards) == 0 {
		check.Detail = "no boolean derived from llama_model_is_recurrent || llama_model_is_hybrid guards the capture path"
		return check
	}
	for _, guard := range guards {
		pattern := regexp.MustCompile(`pos_min\s*=\s*` + regexp.QuoteMeta(guard) + `\s*\?\s*pos_max\s*:\s*pos_min\b`)
		if pattern.MatchString(body) {
			check.Passed = true
			check.Detail = fmt.Sprintf("checkpoint position is captured as pos_max when %s holds", guard)
			return check
		}
	}
	check.Detail = fmt.Sprintf("no recurrent guard (%s) makes the capture path record pos_max as the checkpoint position, so a restore would place recurrent state at the range minimum",
		strings.Join(guards, ", "))
	return check
}

// generationRetentionCheck confirms checkpoints created while generating
// survive the end of the request. Tokens generated on one turn are the prefix of
// the next, so discarding those checkpoints is what turns an agentic loop back
// into a full re-prefill even when selection is correct. Upstream decides this
// with a position comparison that is unsatisfiable on a recurrent model; the
// fork decides it on the token ledger, which is position-layout independent.
func generationRetentionCheck(sources sourceSet) Check {
	check := Check{
		ID:    "checkpoint-generation-retention",
		Kind:  KindStructural,
		Title: "checkpoints taken during generation survive into the next turn",
	}
	body := sources.file("tools/server/server-context.cpp")
	release := findFunctionBody(body, "void release()")
	if release == "" {
		check.Detail = "the slot release path could not be located in tools/server/server-context.cpp"
		return check
	}
	if regexp.MustCompile(`pos_min\s*>\s*\w*prompt_end`).MatchString(release) {
		check.Detail = "the release path still erases checkpoints by comparing pos_min against the prompt end, which discards every recurrent generation checkpoint"
		return check
	}
	if !strings.Contains(release, "checkpoints") {
		check.Detail = "the release path no longer mentions checkpoints, so its retention behaviour is unknown"
		return check
	}
	if !regexp.MustCompile(`n_tokens\s*>\s*\w*(prompt_end|retained_end)`).MatchString(release) {
		check.Detail = "checkpoint retention at release is not decided on the token ledger, so it cannot be shown to be position-layout independent"
		return check
	}
	check.Passed = true
	check.Detail = "generation checkpoints are retained unless they fall outside both the request prompt and the retained token ledger"
	return check
}

// qualificationFlags is every option the first qualification profile emits.
// Generation already refuses a flag the built binary does not list in --help;
// this is the same guarantee moved earlier, so a commit that cannot serve the
// profile is rejected before it is compiled for an afternoon.
var qualificationFlags = []string{
	"--model", "--host", "--port", "--alias", "--device", "--split-mode",
	"--gpu-layers", "--flash-attn", "--ctx-size", "--batch-size", "--ubatch-size",
	"--cache-type-k", "--cache-type-v", "--parallel", "--cont-batching",
	"--no-context-shift", "--kv-unified", "--threads", "--threads-batch",
	"--cache-ram", "--ctx-checkpoints", "--checkpoint-min-step",
	"--no-cache-idle-slots", "--spec-type", "--spec-draft-n-max",
	"--override-tensor", "--jinja", "--warmup", "--metrics", "--no-webui",
	"--api-key-file", "--log-disable",
}

func requiredFlagsCheck(sources sourceSet) Check {
	check := Check{
		ID:    "qualification-flags-present",
		Kind:  KindStructural,
		Title: "the pinned commit defines every option the qualification profile emits",
	}
	body := sources.file("common/arg.cpp")
	missing := make([]string, 0, len(qualificationFlags))
	for _, flag := range qualificationFlags {
		if !strings.Contains(body, `"`+flag+`"`) {
			missing = append(missing, flag)
		}
	}
	if len(missing) > 0 {
		check.Detail = fmt.Sprintf("options absent from common/arg.cpp: %s", strings.Join(missing, ", "))
		return check
	}
	check.Passed = true
	check.Detail = fmt.Sprintf("all %d options the profile emits are defined", len(qualificationFlags))
	return check
}

// forkDefaults are the settings whose fork default differs from the value the
// first qualification needs. Every one of them is a variable that would silently
// separate the fork run from the upstream baseline, which is the one thing the
// A/B comparison cannot tolerate.
var forkDefaults = []struct {
	symbol string
	fact   string
	// mustPin is the manifest field that has to carry an explicit value. It is
	// reported for the operator and asserted by Assert-V2ManifestSemantics.
	mustPin string
}{
	{symbol: "cache_ram_mib", fact: "host prompt cache size in MiB", mustPin: "cache_ram_mib"},
	{symbol: "cache_idle_slots", fact: "idle slots saved to the prompt cache", mustPin: "cache_idle_slots"},
	{symbol: "n_ctx_checkpoints", fact: "context checkpoints per slot", mustPin: "ctx_checkpoints"},
	{symbol: "checkpoint_min_step", fact: "minimum spacing between checkpoints", mustPin: "checkpoint_min_step"},
}

var cacheTypeClearsVBR = regexp.MustCompile(`vbr_cache_type_[kv]\s*=\s*common_vbr_is_alias\s*\(\s*value\s*\)`)

// controlVariableCheck reads the defaults the fork ships and confirms an
// explicit KV type still turns its variable-bitrate cache off.
//
// This is what keeps the first comparison honest. The fork enables a host prompt
// cache, idle-slot saving, and dynamic VBR by default; upstream at the pinned
// baseline does not. Adopting the fork without pinning those would change four
// variables at once and then attribute the result to context checkpoints. The
// defaults are recorded rather than judged - the manifest is where they get
// pinned - but VBR is different, because there is no manifest field for it: the
// only lever is that an explicit `--cache-type-k q4_0` clears the alias, so that
// relationship is asserted here.
func controlVariableCheck(sources sourceSet, facts map[string]string) Check {
	check := Check{
		ID:    "control-variables-pinnable",
		Kind:  KindStructural,
		Title: "every fork default that would separate the A/B run can be pinned away",
	}
	header := sources.file("common/common.h")
	unknown := make([]string, 0, len(forkDefaults))
	read := make([]string, 0, len(forkDefaults))
	for _, setting := range forkDefaults {
		value := findMemberDefault(header, setting.symbol)
		if value == "" {
			unknown = append(unknown, setting.symbol)
			continue
		}
		facts[setting.symbol] = value
		read = append(read, fmt.Sprintf("%s=%s", setting.symbol, value))
	}
	if len(unknown) > 0 {
		check.Detail = fmt.Sprintf("the shipped default could not be read for: %s; a profile cannot pin what it cannot see", strings.Join(unknown, ", "))
		return check
	}
	if !cacheTypeClearsVBR.MatchString(sources.file("common/arg.cpp")) {
		check.Detail = "an explicit --cache-type-k/--cache-type-v no longer clears the variable-bitrate KV alias, so the fork's own KV format cannot be held constant against the upstream baseline"
		return check
	}
	facts["vbr_cleared_by_explicit_cache_type"] = "true"
	check.Passed = true
	check.Detail = fmt.Sprintf("shipped defaults read (%s); an explicit KV cache type clears the variable-bitrate alias",
		strings.Join(read, ", "))
	return check
}

// findInitializer returns the initializer expression of the named variable,
// searching the whole file rather than a known offset.
func findInitializer(body, name string) string {
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*`)
	location := pattern.FindStringIndex(body)
	if location == nil {
		return ""
	}
	remainder := body[location[1]:]
	end := strings.Index(remainder, ";")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(remainder[:end])
}

// ternaryIntegers pulls the two integer literals out of `cond ? A : B`, in that
// order. It refuses anything that is not exactly that shape so a more complex
// expression is reported as unreadable rather than guessed at.
func ternaryIntegers(expression string) (int, int, bool) {
	question := strings.LastIndex(expression, "?")
	if question < 0 {
		return 0, 0, false
	}
	colon := strings.Index(expression[question:], ":")
	if colon < 0 {
		return 0, 0, false
	}
	colon += question
	first, firstErr := strconv.Atoi(strings.TrimSpace(expression[question+1 : colon]))
	second, secondErr := strconv.Atoi(strings.TrimSpace(expression[colon+1:]))
	if firstErr != nil || secondErr != nil {
		return 0, 0, false
	}
	return first, second, true
}

// recurrentGuardNames returns every boolean initialized from the
// recurrent-or-hybrid predicate, which is how the fork spells "this model's
// state cannot be rewound". There is more than one - the selection path and the
// capture path each derive their own - so callers test all of them rather than
// assume the first one found is the relevant one.
var recurrentGuard = regexp.MustCompile(`\bbool\s+(\w+)\s*=\s*llama_model_is_recurrent\s*\([^)]*\)\s*\|\|\s*llama_model_is_hybrid\s*\([^)]*\)`)

func recurrentGuardNames(body string) []string {
	collapsed := regexp.MustCompile(`\s+`).ReplaceAllString(body, " ")
	var names []string
	seen := map[string]bool{}
	for _, match := range recurrentGuard.FindAllStringSubmatch(collapsed, -1) {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		names = append(names, match[1])
	}
	return names
}

// findFunctionBody extracts the braced body that follows a signature, balancing
// braces so a nested lambda does not truncate it.
func findFunctionBody(body, signature string) string {
	start := strings.Index(body, signature)
	if start < 0 {
		return ""
	}
	open := strings.Index(body[start:], "{")
	if open < 0 {
		return ""
	}
	open += start
	depth := 0
	for index := open; index < len(body); index++ {
		switch body[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[open : index+1]
			}
		}
	}
	return ""
}

var memberDefault = regexp.MustCompile(`(?m)^\s*(?:[\w:<>, ]+?)\s+%s\s*=\s*([^;]+);`)

// findMemberDefault reads the default of a struct member out of a header.
func findMemberDefault(header, symbol string) string {
	pattern := regexp.MustCompile(strings.Replace(memberDefault.String(), "%s", regexp.QuoteMeta(symbol), 1))
	match := pattern.FindStringSubmatch(header)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}
