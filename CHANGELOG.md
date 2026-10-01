# Changelog

All notable changes are documented here. This project follows Keep a Changelog conventions and will use semantic versioning once v2 leaves canary.

## [Unreleased]

### Changed

- Claude Local opens beside the signed-in Claude Desktop instead of replacing
  it (ADR 0022). The flyout's `Claude Local` button replaces the
  Anthropic/Local switch, which closed every Claude Desktop process to change
  mode. The 3P selector is held at `3p` only while one launch reads it and
  rests at `1p`; instances are told apart by their helpers'
  `--user-data-dir`. No CIA code path can stop a Desktop process any more.
  `cia-tray -claude-mode/-apply` become `-claude-open` and `-claude-local`,
  and `Configure-ClaudeDesktop.ps1` takes `-Instance Anthropic|Local`.
  With no model loaded, `Claude Local` first loads the model Desktop's
  ten-second start-up health check asks for, so the instance no longer opens
  on "Não foi possível alcançar 127.0.0.1:18090".
- The flyout's model radio follows the model in memory: when the loaded model
  changes, whoever loaded it (the tray, Claude Local, `/local`), the radio
  moves to it, and it returns to the saved choice when the model unloads. The
  saved choice is only changed by a click.
- A model that admission refuses gets an explanation in Portuguese: the
  shortfall in GiB, the headroom already counting what unloading the current
  model frees, and, for a memory shortfall, the three applications holding the
  most RAM. `/v1/messages` answers it as `400 invalid_request_error`, because
  Claude Desktop retried the former `503` ten times and showed only
  "Solicitação falhou"; the OpenAI routes keep `503 insufficient_capacity`.
- The `local_ai_delegate` tool description no longer calls the pinned model a
  9B executor, and says to leave `max_output_tokens` unset: a reasoning model
  can spend a small cap entirely before it answers.
- IA Local (`cia-tray`) is the deployment's one startup entry (ADR 0021).
  `Install-V2PanelStartup.ps1` registers a current-user `Run` value that Task
  Manager lists as "IA Local" with its icon, adds an "IA Local" Start-menu
  shortcut, and removes the legacy Startup shortcut that ran the tray through
  `wscript.exe` and showed as "Windows Script Host". `Install-V2ScheduledTasks.ps1`
  registers Router and Edge without a logon trigger; the tray starts them when
  it opens, through the Task Scheduler COM API, and "Encerrar" ends them after a
  confirmation. Disabling IA Local in Task Manager disables the whole system at
  logon.
- The tray's native menu and Win32 control window are replaced by one flyout
  drawn from the browser monitor's design tokens: status pill, state card,
  model radio list with load/switch/unload, a `Claude Local` button,
  "Abrir painel" and "Encerrar". It follows the
  Windows app theme, scales per monitor, works from the keyboard, and the icon
  takes the monitor's mark coloured by state. `cia-tray.exe` now carries an
  icon, a version resource and a per-monitor-v2 DPI manifest.
- "Abrir painel" starts `cia-monitor` on demand in a job that ends with the
  tray. A second start of IA Local opens the running tray's flyout.

### Removed

- The flyout's Codex and OpenCode buttons, `panel.Launcher` and its capability
  checks: both harnesses reach this server only through `/local` or the
  manual launcher scripts. `launchers` in a generated panel configuration is
  still read and no longer validated.
- Tray model folders, GGUF detection, hash validation and `-validate-model`
  (a detected GGUF could never be loaded without a manifest profile), the
  "Atualizar" and "Detalhes do status" entries, and the unused model-submenu
  map. `model_roots_path` and `validation_path` are still accepted in the panel
  configuration and ignored; `New-V2Config.ps1` stops writing them.

### Fixed

- The tray no longer fails to start when the saved selection names a model the
  deployment stopped serving: it falls back to the public model and says so.
  A missing launcher script now fails only its own button.
- The Codex launch the tray implemented but never offered is a flyout button.
- The icon's tooltip is shown (NOTIFYICON_VERSION_4 needs `NIF_SHOWTIP`), and
  the Claude gateway check runs at most once a minute instead of on every
  refresh.
- A reachable edge that is not ready because its default model does not fit is
  reported as such, with the reason, instead of "not ready"; the
  `insufficient_physical_memory`, `insufficient_vram_budget` and
  `resource_profile_incomplete` capacity reasons are translated.
- The README no longer says the tray drains and resumes the provider; that is
  `cia-mcp-admin` and the release transaction.

- The browser monitor uses the context window recorded with each historical
  request, so changing or unloading an external model cannot change an old
  request's occupancy. An unknown historical window stays unknown.
- A monitor restart clears the page's old operation state; a late POST reply
  from the previous instance cannot restore it. Action receipt, including the
  response body, has a three-second deadline. After an uncertain result the
  page waits for a fresh snapshot before admitting another action and never
  retries a mutation automatically.
- Removed the unused log-selection expression and console command reexport
  module, and corrected error-string capitalization flagged by Staticcheck.

### Added

- Browser-monitor lint and DOM regression tests run in CI using the console's
  existing development dependencies, without contacting a real service.
- The monitor now publishes a bounded `requests[]` feed that combines measured
  edge requests and external llama.cpp log requests without treating GPU bursts
  as requests. Each count or rate carries its source (`runtime-usage`,
  `runtime-timings`, `server-log`, derived log value, or estimate); absent values
  remain null. `coverage` reports separately how many detected external sources
  have per-request measurements and how many have activity only. The page marks
  estimated rates and explains derived cache counts. When an external model is
  loaded while admission lacks physical memory, it points to the existing
  confirmed stop action instead of acting automatically. Stream events counted
  without runtime usage are kept separate from exact output-token totals.
- The monitor measures requests of tools that bypass the edge. A llama.cpp
  server - the engine under LM Studio and Bionic, or one started by hand with
  `--log-file` - writes each request's counts to its log, and the monitor now
  reads them: prompt, cached tokens, output, prompt-processing and generation
  speed, time to first token, context in use and the board's energy in the
  period. Prompt and cache are derived from the counts the server itself logs;
  they matched its `usage` and `timings` in the measured sample, and only numbers
  are read, never text
  (ADR 0020, section 9). The requests table now lists the edge's requests and
  these together, each with a "via" line, and the speed, context, cache and
  totals figures follow the newest measured request from either. Servers with no
  request log (Ollama and others) still show activity, GPU and energy, with a
  note that says why there are no counts.
- A protected server's refusal is remembered (60 s for 401/403, 30 s otherwise)
  instead of being asked again every two seconds, which had filled Bionic's own
  log with a 401 per pass.
- The monitor shows more of what other tools do and can unload their models. For
  LM Studio / Bionic's own llama-server, whose API is behind a key of its own,
  the model, quantization, window and slots come from the process's launch
  arguments (an allowlist of six flags; the rest of the line, key included, is
  never kept or used), beside its RAM, its start time and the GPU it holds.
  Stretches of activity of tools the edge cannot see - duration, peak GPU and
  power, the board's energy in the period - are listed in the requests table as
  "externa". A "Encerrar processo do modelo" button on each source ends the
  process that holds its model after a native confirmation that defaults to
  Cancel; the page names a source and never a pid, the process is re-identified
  by name and creation time before it is ended, and a fixed list of system,
  desktop and deployment programs is never ended (ADR 0020, sections 6 to 8).
- The models list follows the disk and the tools, not only the manifest. The
  edge's `/api/v1/status` now reports, for each model, `artifact.present` and
  `artifact.size_matches` - from the weights file's metadata, never its path -
  and a model whose file is gone or the wrong size is `available: false` with
  the reason `artifact_missing` or `artifact_size_mismatch`; a file the edge is
  not permitted to stat is "unknown", not missing. The monitor shows it as
  "Arquivo ausente" and disables loading. Its Modelos tab also lists what the
  other tools on the machine hold - for LM Studio / Bionic and Ollama, the
  installed library as well as the loaded models - read from each tool's own
  API.
- `capabilities.reasoning` is a declared, optional capability in the manifest
  schema (it existed in the edge's types but not in the schema, so the monitor's
  "Raciocínio" tag could never light). It is `true` for the four active models,
  on the evidence of the qualification runs, where `reasoning_content` was
  present in 31 of 33 cases for Gemma, 32 of 33 for Qwen3.6 and 37 of 37 for
  each Qwen3.8 profile. It gates nothing. The monitor's capability tags now say
  in their tooltips that a struck-through Ferramentas, Responses or JSON
  estruturado means "not guaranteed", not "incapable" (docs/MODEL_PROMOTION.md).
- The monitor describes the machine, not only the edge (ADR 0020). New cards
  report the GPU's power draw, the energy accumulated since the monitor started
  and its temperatures and clocks, read from AMD's driver library; index 73 of
  its power-management log was identified by measurement (70 W at the desktop,
  about 280 W while a model generated). A list shows which processes hold the
  GPU, whatever program they are. A discovery pass finds other inference tools
  on loopback - LM Studio / Bionic, Ollama, a stand-alone llama.cpp server and
  OpenAI-compatible servers - with GET only and no credential, and reports the
  models they have loaded, their quantization and window, and whether they are
  working; llama.cpp's slots also give a live token rate. When another tool is
  using the GPU the page says so (phases `external` and `external_ready`)
  instead of "Ocioso", and it keeps describing the machine when the edge does
  not answer. Off AMD hardware the power cards read as unavailable.
- Evaluated and rejected on 2026-09-29: yuxinlu1's agentic fine-tune of Gemma 4
  12B (Q4_K_M, Apache-2.0) at 262144 tokens. Admissible at 256k, retention 0.96
  to 196k and 0.875 at 240k, 27/33 on the quality suites in a third of the time
  because it reasons far less, but it rewrites literal tool arguments (3 of 3
  failures) and its lead over the QAT does not survive the QAT's reasoning
  budget. The manifest entry and the GGUF were removed; the campaign evidence
  and `docs/reports/GEMMA4-AGENTIC-QUALIFICATION-20260929.md` stay.
- `cia-monitor` (`cmd/cia-monitor`, `internal/monitor`): a read-only browser
  monitor in the spirit of Strata's Monitor tab (ADR 0019). One page, refreshed
  every second: the request's phase (queued, loading the model, reading the
  prompt, generating) with live output, tokens per second, time to first token
  and progress against the output ceiling; GPU utilization, VRAM, shared memory
  with the edge's paging verdict, CPU, RAM, commit and disk with two-minute
  sparklines; a context gauge with cache reuse and the compaction threshold;
  the recent requests with totals; the model roster; and the client base URLs.
  It listens on a literal loopback address (`127.0.0.1:18095` canary, `:8095`
  final), answers only its own Host names, emits a strict
  Content-Security-Policy and holds no credential.
- Model controls in the monitor: load the chosen model (the pipe's `switch`)
  and unload the loaded one, from the Monitor tab or a model's card. Requests go
  over the ADR 0015 administrative pipe, answered only by the installed
  `cia-edge.exe`, and only after four checks: the request comes from the
  monitor's own page (exact `Origin`, `Sec-Fetch-Site`, custom header, JSON);
  the connection's owning process runs as the monitor's user, read from the
  kernel's TCP table; the operator approves a native dialog that names the
  operation and model, defaults to Cancel and expires after 45 s; and the model
  is one the edge lists, with one operation at a time and none while inference
  holds the gate. `-admin-pipe off` removes the buttons. Exercised on the
  reference workstation: a load confirmed in the dialog had the model serving
  in five seconds.
- `Build-V2Binaries.ps1` builds, tests and stages `cia-monitor.exe` with the
  other components. It is not yet part of the approved release set, so
  `Complete-V2Deployment.ps1` does not install it.
  The page is hand-written HTML, CSS and JavaScript compiled in with
  `go:embed`, with no framework or npm tree; tests fail on any HTML sink in the
  script or inline script and style in the page. Machine counters come from PDH
  through `PdhAddEnglishCounterW` (GPU utilization as Task Manager defines it),
  `GlobalMemoryStatusEx`, the llama-server process, and D3DKMT for the
  adapter's name and size. Measured cost: about 0.5% of one core and 28 MiB.
- `GET /api/v1/inference` on the edge's control listener: the requests holding
  an admission slot and the last 64 finished ones, as numbers only — prompt,
  cached and output tokens, prompt and decode rates, time to first token,
  duration, status and finish class — plus totals since start. The counts are
  llama-server's own `usage` or `timings`, read from the response on its way to
  the client without altering a byte; a streamed answer with neither is counted
  by token events and marked `output_estimated`. Successful reads are not
  recorded in `recent_events`, so polling does not evict the event log.
- `model_statuses[].process_state` in `/api/v1/status`: the router's state for
  each model's process, restricted to its known words, so a model being loaded
  can be told from a long prompt.
- `benchmarks/agentic-reuse-b10549-20260928/`: the agentic incremental-reuse
  gate run against the pinned upstream runtime (b10549) with the
  `qwen38-27b-agent-128k` and `qwen36-35b-a3b-huge-256k` manifest entries. All
  six cells pass Gates B, C and D (~55k to ~257k tokens, 98% of the Huge
  window) with zero full re-prefills: a turn processes its increment, or about one `ubatch` when the
  harness rewrites the last assistant turn — the result ADR 0010 assumed
  upstream could not produce. `Run-AgenticReuse.ps1` in the same directory
  starts a manifest profile through the production argument builder, refuses to
  run beside any existing `llama-server`, stops only the process it started,
  and rebuilds a summary from saved evidence with `-SummarizeOnly`.
- Second llama.cpp runtime for Qwen3.8 agentic long context, built from a pinned
  `spiritbuun/buun-llama-cpp` commit (ADR 0010). The upstream build is untouched
  and remains the baseline and the fallback; the fork is a `candidate`,
  experimental, and initially exclusive to Qwen3.8-27B. `engine` stays
  `llama.cpp` because the fork preserves the `llama-server` interface, so
  llama-swap remains the only lifecycle authority and no runtime abstraction was
  introduced. Retiring it is a manifest edit.
- Typed runtime provenance in `config/models.schema.json`: `variant`
  (`upstream`/`fork`) and a closed `provenance` object carrying source
  repository, a full 40-hex `source_revision`, optional upstream ancestry,
  checkpoint-fix evidence with the gate-report hash, and the build configuration
  (backend, GPU targets, Release, `llama-server` only). Branch names, tags and
  `HEAD` cannot be expressed; `additionalProperties: false` stays closed
  throughout and no `extra_args` or metadata map was added.
- `cia-fork-gate` (`cmd/cia-fork-gate`, `internal/forkgate`): refuses to accept a
  fork commit on the strength of a patch being present. It compiles the fork's
  own checkpoint-selection predicate out of the pinned tree and asserts a
  semantic invariant — for recurrent and hybrid models the verdict must not vary
  with `pos_min` or the position threshold anywhere in their range, must turn on
  the recurrent frontier instead, and must leave transformer selection unchanged
  — which is the correction discussed in `ggml-org/llama.cpp#22384`. Structural
  checks cover short-prompt capture, frontier-accurate capture, retention of
  generation checkpoints, the option set the profile needs, and the fork's
  shipped defaults. The gate fails closed, including when no compiler is
  available, and its report hash is recorded in the manifest.
- `Build-V2ForkRuntime.ps1`: pins the commit, verifies the checkout, runs the
  provenance gate before compiling anything, builds Release/ROCm/`gfx1201`/
  `llama-server` only, installs into a directory carrying the commit, refuses to
  write where an existing manifest runtime lives, hashes the artifact, re-checks
  the binary's own `--help`, and prints the manifest entry for review.
- `Measure-V2AgenticReuse.ps1`: the agentic incremental-reuse regression from
  `BENCHMARKS.md` as an executable gate. It grows a synthetic, seeded context by
  small increments interleaved with tool calls and records per turn the context
  size, new versus processed prompt tokens, reuse ratio, prefill and decode
  throughput, turn latency, MTP acceptance and memory — from the server's own
  `cache_n`/`prompt_n` counters, never from a rate. Emits
  `incremental_reuse_pass` or `incremental_reuse_fail` and a non-zero exit,
  plus `agentic_turn_efficiency` as supporting evidence.
- `Compare-V2Runtimes.ps1`: upstream-versus-fork A/B that refuses two reports
  whose fixture hash, seed, context, increment, turn count, output cap or
  threshold differ, so a table can only be produced from a real comparison.
- `Test-V2AgenticHarness.ps1`: exercises the measurement and comparison scripts
  against scripted servers in the reusing and re-prefilling states, with no GPU
  and no model, because a gate that cannot fail is not a gate.
- Runtime observability in `/api/v1/status`: a `runtimes` block and per-model
  runtime identity (id, state, engine, variant, backend, commit, abbreviated
  artifact hash, checkpoint capability), configured context, and checkpoint
  configuration. Installation paths, credentials, prompts and responses are
  deliberately absent, and `/v1/models` is unchanged.
- Buun qualification profiles and sweeps in `model-test-matrix.json` (gates A
  through D, plus an upstream control at identical settings), with the checkpoint
  count/spacing, `spec_draft_n_max` and near-256k context sweeps recorded as
  sweeps rather than as settled values.
- `Resolve-V2QualificationRequestBudget` in `Common.ps1`: one source of truth for
  the HTTP `max_tokens` a qualification run may request. Explicit `-MaxTokens`
  wins, otherwise a positive `n_predict` -- the profile's own generation contract
  -- becomes the ceiling, otherwise each suite keeps its fixture default. It
  shares the answer-reserve invariant with `Assert-V2ManifestSemantics` through a
  new `Test-V2AnswerReserve`, so a profile the manifest accepts cannot become a
  benchmark that is impossible to run.
- `-DryRun` on `Invoke-V2ProfileQualification.ps1`: resolves the budget, builds
  both the llama-server and `qualify.py` command lines, prints them and stops
  without loading anything. A multi-hour cell can be reviewed -- and asserted on
  by a fast test -- before the night it costs hours.
- `-ConstrainedRequestBudgetDiagnostic`: the named way to measure a request cap
  below the profile's answer reserve on purpose. Without it the combination is
  refused before the model loads; with it the report is stamped as a diagnostic.
- `request_budget` in every qualification report (effective ceiling, its origin,
  reasoning budget, answer reserve) and `policy_profile` (`baseline` or
  `diagnostic`) in `qualify.py` output, so a future `NO_ANSWER` is attributable
  to either the deployed contract or a named benchmark cap.
- `scripts/v2/eval/test_tool_grading.py` and `scripts/v2/eval/test_qualify_budget.py`:
  pure-Python self-tests, no server and no toolchain, wired into CI. They pin the
  budget arithmetic, the retention context clamp, the failure taxonomy, nested
  and literal tool-argument grading, and -- the regression that matters -- that
  every literal a fixture grades byte-exactly is supplied byte-exactly in its
  prompt.
- Named `unity_impl` regressions in `test_verifiers.py`: a known-good
  `ProjectilePool` compiles against `UNITY_SHIM`; the answer
  `qwen38-27b-deep-32k` produced on 2026-08-23 does not, and the toolchain output
  must name `CS0136`; the same answer with the scope repaired compiles again.
  The 2026-08-23 verdict is now falsifiable by running something rather than by
  re-reading a report.
- Argv-quoting regression in `Test-V2ConfigGeneration.ps1`: a multi-word
  `reasoning_budget_message` must survive command construction as one argv token,
  through both the benchmark and the production path. A regression that
  re-introduces the raw-array pattern makes `budget` a token of its own again,
  which is what killed the first Huge-256k launch.
- `internal/panel/capability_contract_test.go`: pins what
  `capabilities.function_calling` means -- required rather than defaulted,
  carried verbatim into the projection, and deliberately not a launch gate --
  and `Test-V2HarnessConfig.ps1` now asserts the Codex catalog's
  `supports_parallel_tool_calls` tracks it exactly in both directions.
- `docs/reports/HARDENING-post-qualification-20260823.md`: the harness changes,
  a staged and explicitly unqualified Gemma reasoning-budget candidate, the
  preserved Qwen3.6-35B-A3B status, and the exact commands for the physical
  re-run. Nothing in it has been run.
- `Get-V2EffectiveGenerationCeiling` / `effective_generation_ceiling`: the
  tokens a run can actually generate, `min(request_max_tokens, n_predict)`.
  llama-server stops at `n_predict` whatever `max_tokens` asks for, so a request
  above it is arithmetic and not headroom, and every budget rule now reads this
  number instead of the request.
- `Get-V2BudgetProfile` / `budget_profile`: `deployment`, `constrained` or
  `expanded`, recorded in every plan and every qualification report. Only
  `deployment` -- the profile measured exactly as it is served -- is a baseline.
- `INSUFFICIENT_CONTEXT_RESERVE`, a canonical failure name for a retention probe
  whose prefill leaves no room for even a floor-sized answer.
- `qualify.py --n-predict`, so the verifier is told the profile's own generation
  contract rather than inferring it from a request the server is about to
  truncate. `Invoke-V2ProfileQualification.ps1` forwards it whenever `-NPredict`
  is set.
- Deterministic regressions for all of the above, in the existing fast gate:
  `CEILING_TABLE`, `BUDGET_PROFILE_TABLE`,
  `test_impossible_expanded_budget_is_refused`, `test_policy_profile` and
  `test_retention_insufficient_context_reserve` in
  `scripts/v2/eval/test_qualify_budget.py`, mirrored by `Assert-EffectiveCeiling`
  and `Assert-BudgetProfile` in `Test-V2ConfigGeneration.ps1`.

### Changed

- The v1 Python stack is removed: `control/` (the HTTP panel), `mcp/` (the
  Python MCP server and its client registrations) and the scripts that started
  or registered them (`local-llama-tray`, `start-local-llama-panel`,
  `install-`/`uninstall-local-llama-startup`, `register-unsloth-mcp`). It was
  already stopped, its Startup shortcut disabled and no client registered it;
  `cia-tray`, `cia-monitor` and `cia-mcp` replace it. The profile benchmark
  scripts and `model-test-matrix.json` stay: they are independent of the panel.
- The roster shown by the monitor, Codex and OpenCode is four models. The
  Ornith 1.5 35B-A3B profile is removed from `config/models.yaml` together with
  its chat templates, their CI contract test and its weights; its campaign
  evidence under `benchmarks/` stays as history. The 128k
  `gemma4-12b-qat-ud-q4xl` alias (the same GGUF as the 256k entry) is `retired`
  with no deployments. Display names now state the model, its
  weights quantization and its context window
  (`Qwen 3.8 27B | UD-Q3_K_XL | 128k context`) instead of role slogans and
  "candidate". The first manifest entry is the 256k Gemma so the semantic tests,
  which mutate `models[0]`, keep exercising an active model. Client catalogs
  were regenerated; an installed edge shows the change after the next release.
- ADR 0019 supersedes the direction of ADR 0018. `cia-console.exe` and
  `frontend/` are frozen: not developed, not deployed, and not deleted, which
  remains the operator's decision. `cia-tray.exe` stays the only surface that
  changes anything; the browser monitor only reads.
- `config/models.yaml` declares `cache_ram_mib: 8192`, `ctx_checkpoints: 32`,
  `checkpoint_min_step: 8192` and `cache_idle_slots: true` on five of the six
  active profiles. These are the runtimes' own defaults, so the served
  behaviour is unchanged, but admission now charges the 8 GiB host prompt cache
  it previously never saw: the Huge profile needs 32.6 GiB of available commit
  instead of 24.6. `gemma4-12b-qat-ud-q4xl` stays undeclared by operator
  decision.
- `provider.public_model` is `gemma4-12b-qat-ud-q4xl-256k`: the same GGUF with
  a 262,144-token window and the reasoning budget that took it from 26/34 to
  31/34 (ADR 0016, addendum). Both Codex profile TOMLs pin the new id and
  `New-V2ClientCatalogs.ps1` regenerated both OpenCode providers; the Codex
  catalog did not change. The public model now needs 26.9 GiB of available
  commit at admission (17.5 before), and a prompt that fills its window pays up
  to 12.7 minutes to first token.
- `cia-mcp-inference` no longer has a built-in model: without
  `CIA_MCP_INFERENCE_MODEL` it fails at startup and names the installer. The
  compiled fallback was `local-coding`, removed from the roster on 2026-08-22,
  and the Codex, Claude Code and OpenCode registrations still pinned it — a
  model the edge's allowlist no longer admits. `Install-V2McpInferenceIntegrations.ps1`
  now pins the installed manifest's `provider.public_model` when `-Model` is
  omitted and refuses any model that manifest does not list as active; it can
  restate the delegate's limits (`-MaxOutputTokens`, `-Timeout`,
  `-Temperature`) instead of resetting them to 4096 tokens and the defaults;
  and it accepts a Codex config whose END marker Codex itself dropped on
  rewrite, while duplicate markers still fail. `cia-mcp-smoke` requires
  `-model`. The integration README states the new pin and marks its
  measurements as taken on the removed model.
- `Test-V2ConfigGeneration.ps1` proves byte stability on a copy of every
  manifest entry stripped of its tuning fields, instead of requiring an untuned
  real entry, and derives its cases from a stripped template.
  `Test-V2Manifest.ps1`'s semantic cases start from a copy whose `models[0]`
  carries no tuning fields. Both used to break as soon as the first entry
  declared a field a case adds (`MemberAlreadyExists`), or every entry declared
  one; verified against a scratch manifest in which all twelve entries do.
- `TUNING.md` §1.4 no longer states that context checkpoints fail on hybrid
  models: it carries the b10549 measurement, keeps #24055/#22384 as history,
  and records that leaving `cache_ram_mib` undeclared does not disable b10549's
  default 8 GiB host prompt cache, which admission therefore never charged.
  §1.2, §1.5, §2 and §3, `BENCHMARKS.md`, ADRs 0009 and 0010 (addenda; statuses
  unchanged) and the `model-test-matrix.json` notes follow it.
- Tool-argument strings are compared byte-for-byte instead of through `norm()`,
  which folded case and stripped whitespace. `Vendor/**` matched `vendor/**` and
  `FOO[0-9]+` matched `foo[0-9]+`, so `literal_identifier` could not see the case
  it asks a model to preserve. Re-graded against all 44 tool rows in the raw
  2026-08-23 arguments this changes no historical verdict. Retention probes keep
  the lenient comparison, where `"Go" == "go"` is correct.
- Three tool fixtures now state every literal the grader demands.
  `tool_pick_search_many_nested` and `literal_regex` said "excluding vendor" while
  grading against `vendor/**`; `literal_cs_glob` demanded `case_sensitive: true`
  -- a required schema field -- from a prompt that never mentioned case. **No
  expected value changed and no verifier was loosened**: `vendor`, `vendor/`,
  `vendor/*` and `vendor/**` remain four different arguments. The 2026-08-23
  scores stand as measured.
- The coding-fixture output cap is the named `coding_tasks.DEFAULT_MAX_TOKENS`
  rather than a literal, and is documented as a floor for a fixture that a
  profile ceiling overrides -- never something to raise to suit one profile.
- Failure taxonomy extended so a transport failure can no longer be read as a
  model result: `REQUEST_TIMEOUT`, `REQUEST_ERROR`, `MODEL_OUTPUT_FAILURE`,
  `TOOL_ARGUMENT_ERROR` and `STRUCTURED_OUTPUT_ERROR` join the existing
  `NO_ANSWER` / `OUTPUT_LENGTH` / `REASONING_EXHAUSTED` set, every suite tags its
  exception rows, and the toolchain-level `TIMEOUT` is renamed `VERIFIER_TIMEOUT`
  to keep it distinct from an HTTP timeout. `COMPILE_ERROR` now also matches
  `dotnet` output in pt-BR, the host locale. Output exhaustion is still never
  converted into a compile failure.
- The retention suite receives the resolved profile ceiling, clamped to the room
  left in the context window and never below its 4096-token floor, and reports
  which of `fixture` / `profile` / `context-clamped` applied.
- An explicit `-MaxTokens` above the profile's `n_predict` no longer resolves as
  though those tokens existed. `-NPredict 8192 -MaxTokens 32768
  -ReasoningBudget 24576` used to compute a reserve of `32768 - 24576 = 8192`
  and pass; the server would have stopped at 8192 with 24576 already spent
  thinking, a reserve of **-16384**. The effective ceiling is now `min()` of the
  two and the combination is refused before the model loads, exactly as
  `-MaxTokens 8192` already was. The operator's request is still recorded
  verbatim beside it -- both numbers are kept.
- `policy_profile` reads `baseline` only when the generation budget was the
  served one too. An explicit ceiling unlike the profile's contract, or
  `--allow-constrained-request-budget`, now makes the cell `diagnostic` the same
  way a non-default system or tool policy always did. The runner warns at plan
  time rather than leaving it to be noticed in the report.
- `resolve_retention_reserve` returns `(0, INSUFFICIENT_CONTEXT_RESERVE)` when
  the prefill leaves less than the 4096-token floor, and `run_retention` sends no
  request at all: the row records the window arithmetic and its taxonomy. It
  previously returned 4096 regardless -- including on the path that never
  consulted the window -- and sent a request the server had to reject for
  exceeding `n_ctx`, which reads like a model failure. The corpus is never
  silently shrunk; a probe at 240k that quietly becomes one at 220k is a
  different measurement wearing the same label.
- `docs/reports/HARDENING-post-qualification-20260823.md` step 5 splits the Gemma
  cell in two. It previously A/B-tested the candidate at 16384/8192 in step 4 and
  then ran the full qualification against the profile *as served today*, which
  qualifies nothing. `gemma4-12b-control-full` stays as the control and
  `gemma4-12b-candidate-full` carries `-NPredict 16384 -ReasoningBudget 8192` in
  every suite; only the latter can qualify the candidate.
- Two wrong claims in the same document are corrected: 8192 is the minimum
  **answer reserve** the output contract chose, not "the smallest default any
  suite uses" (`tools`, `literal_tools`, `json` and `retention` all default to
  4096); and step 3 does not issue 4096-token requests, because a profile's
  `n_predict` raises the request ceiling to 8192 or 32768.
- `capabilities.function_calling` is documented in the schema and in
  `MODEL_PROMOTION.md` as a per-artifact deployment guarantee proven through
  `internal/edge/namespace.go`, not a description of what a chat template can do.
  `gemma4-12b-qat-ud-q4xl` scoring 6/7 on the tool suite against llama-server's
  own endpoint is a different measurement and the flag stays `false`.
- `docs/reports/QUALIFICATION-CAMPAIGN-20260823.md` carries an appended errata
  section. The measured tables above it, and every file in
  `benchmarks/campaign-20260823-full/`, are unchanged.
- `cache_idle_slots` is three-valued in the generator. Absent still emits
  nothing, so every model generated before the field existed keeps a
  byte-identical command line; a declared value now emits `--cache-idle-slots`
  or `--no-cache-idle-slots`. Silence cannot disable a runtime default that is
  on, which is the case on the fork.
- `Assert-V2ManifestSemantics` refuses a model on a fork runtime that leaves
  `context_shift`, `kv_unified`, `cache_ram_mib`, `cache_idle_slots`,
  `ctx_checkpoints` or `checkpoint_min_step` to the runtime's own defaults; a
  fork runtime without provenance or pinned to a moving reference; provenance
  declared without `variant: fork`; and a `provider.public_model` served by a
  fork runtime that is not qualified or enabled.

### Unchanged, deliberately

- The upstream llama.cpp runtime entries, their hashes, paths, environments, and
  every model bound to them, including all 4B/9B/12B behaviour and
  `provider.public_model`. `Test-V2ConfigGeneration.ps1` still proves the
  generated command line is byte-identical for all six existing models.
- Admission control. The fork profile offloads tensors, so it fails closed with
  `resource_profile_incomplete` until its VRAM, RAM and commit peaks are measured
  on the target GPU; there is no fork branch in `capacity.go`.
- `provider.max_loaded_models = 1`, `parallel = 1`, the loopback-only boundary,
  the credential and ACL model, llama-swap as lifecycle authority, and the
  absence of any cloud fallback.
- The prompt cache and `/slots` persistence stay out of scope: `cache_ram_mib` is
  pinned to `0` and `cache_idle_slots` to `false` on the fork profile. Context
  checkpoints and prompt caching are different mechanisms, and only the first is
  being qualified. VBR, TurboQuant, TCQ and the fork's own KV types stay off.

- Go-based v2 edge, MCP, and Windows Credential Manager helper foundations.
- Single provenance- and checksum-based model/runtime manifest with JSON Schema.
- llama-swap v240 template with lazy loading, 900-second TTL, router authentication, no aliases, one active model, and no retained log buffer.
- Canary/final configuration generator and hidden scheduled-task launchers, all preview-only by default.
- Installation, artifact, listener, side-effect, and secret-pattern verification scripts.
- Architecture, threat model, runbook, promotion, benchmark, security, contribution, licensing, and ADR documentation.
- Windows CI for Go quality gates, PowerShell parsing, manifest validation, vulnerability analysis, and secret scanning.
- Native Win32 `cia-tray` operator panel with live status, explicit lifecycle
  operations, atomic selected-model state, capability-aware harness launchers,
  single-instance protection, and no new listener or UI framework dependency.
- Supported Unsloth canary/final launchers that leave private Studio state
  untouched and never restore the retired external connection.
- Approved-hash, atomic installers for staged Edge/MCP/tray binaries and an
  approved-plan transactional harness installer.
- Separate `cia-mcp-inference` bridge with one bounded `local_ai_delegate`
  tool, direct Credential Manager authentication, and provider-preserving
  global integrations for SOTA Codex, Claude, and OpenCode sessions.
- Metadata-only MCP live probe, DPAPI-backed transactional client registration,
  and an idempotent current-user Startup shortcut for the native panel.
- Python quality-eval and long-context/tool-call stress-eval scripts
  (`scripts/run-profile-quality-eval.py`, `scripts/run-profile-stress-eval.py`),
  mirroring the existing PowerShell quality harness for cross-language reuse
  and adding a needle-in-haystack plus forced-tool-call test the PowerShell
  suite did not have.

- Manifest support for models too large to fit entirely in VRAM: optional typed
  fields `context_shift`, `kv_unified`, `cache_ram_mib`, `ctx_checkpoints`,
  `checkpoint_min_step`, `cache_idle_slots`, `spec_decoding`, and
  `tensor_overrides`, plus `runtimes[].device.vram_mib`. `additionalProperties`
  stays closed; a generic `extra_args` escape hatch was deliberately rejected
  (ADR 0009).
- `Test-V2ConfigGeneration.ps1`, run in CI, proving that a model declaring none
  of the new fields still generates a byte-identical `llama-server` command line,
  so growing the schema never rewrites a published deployment.
- VRAM dimension in edge admission control: a measured `peak_vram_gib` plus the
  documented 1 GiB reserve is checked against the runtime's declared device
  budget, reported as `insufficient_vram_budget`.
- Physical-RAM dimension in edge admission control, reported as
  `insufficient_physical_memory`. `GlobalMemoryStatusEx` was already being called
  and `AvailablePhysical` already sat in the struct, discarded; `peak_ram_gib`
  likewise already existed in the schema and was never read. Commit bounds what
  may be reserved, physical bounds what may stay resident — for a model with
  weights offloaded to system RAM, only the second predicts throughput.
- Measurement rules for the resource envelope in `docs/MODEL_PROMOTION.md` and
  `docs/BENCHMARKS.md`: `peak_commit_gib` is a delta and must be recorded with
  its idle baseline (`idle_commit_gib`) and with a **cold prompt cache**, since
  admission adds `cache_ram_mib` separately as its full ceiling. Without that
  rule the same gibibytes were charged twice.
- `docs/TUNING.md`: bottleneck diagnosis and tuning. Gives the memory-bandwidth
  ceiling as arithmetic so a measured rate can be judged against its hardware
  limit instead of intuition, a symptom-ordered decision tree, and the tuning
  levers ranked by effect. Quantifies what offload actually costs on this
  platform: the first gibibyte moved off the GPU costs ~37% of decode
  throughput, and a fully resident smaller quant can be ~4.7x faster than an
  offloaded larger one. Section 1.1 works through speculative decoding: MTP
  amortizes weight reads but not arithmetic, so on a CPU-resident portion the
  optimal draft depth collapses to 2-3 and a depth of 7 can be *slower* than not
  speculating at all. Includes a sensitivity table over CPU GEMM throughput, the
  one constant this repository has never measured.
- `docs/TUNING.md` §1.2: how to estimate the VRAM/RAM/commit envelope from the
  model card plus the one measured anchor, before downloading anything. Surfaces
  that Windows commit tracks **VRAM**, not RAM — the 9B consumed 8.06 GiB of
  commit against 6.66 GiB of VRAM with zero CPU-resident weights, so a 27B costs
  ~16 GiB of commit even with no offload and no prompt cache. Also shows the
  template's 96k / KV `q8_0` split needs ~5.1 GiB offloaded rather than 4.4, and
  that 128k with `q4_0` KV needs *less* offload than 96k with `q8_0`.
- `docs/TUNING.md` §1.3: the quality/speed frontier for choosing a quantization.
  Community IQ4_XS builds of this model span roughly a gibibyte, which on this
  hardware is ~60% of decode throughput, so build selection outweighs every
  serving flag. Records that `Q3_K_XL` on 27B-class Qwen measures above 0.1 KLD
  with 85-90% top-token agreement, against a 0.08 "quality drops" threshold, so
  dropping a level is a real cost rather than a free win. Also documents two
  silent MTP killers: quantization tooling dropping the `nextn` head entirely,
  and draft acceptance collapsing at particular `--ctx-size` values on a
  ~2048-token period (llama.cpp #23658).
- The `blk.64` rule, the most consequential per-tensor fact about this model: the
  MTP head must be quantized to Q5_K or higher. Reported measurements put a
  Q4_K MTP block at **0% draft acceptance** — speculation fails completely and
  silently — against 73-74% for builds keeping it at Q5_K-Q8_0. The nominal
  quantization label does not reveal which you have, so `gguf-dump` verification
  is now a precondition in `RUNBOOK.md` §11.0b and `BENCHMARKS.md`. This also
  reverses earlier guidance to prefer the most compact build: compact builds are
  precisely the ones likely to have quantized `blk.64` down.
- `Qwen/Qwen3.8-27B` (Alibaba, Apache-2.0) recorded as the authoritative
  reference for the family across `TUNING.md`, `RUNBOOK.md`, and
  `model-test-matrix.json`. Every GGUF is a derived artifact whose publisher,
  revision, and per-tensor choices are recorded and verified separately; the
  matrix no longer names a GGUF publisher until one passes the `blk.64` check.
- `docs/RUNBOOK.md` §11.0b: choose the build and verify the MTP head survived
  quantization before downloading or benchmarking anything.
- `docs/TUNING.md` §1.4: context checkpoints are upstream-reported non-functional
  on hybrid/recurrent models (llama.cpp #24055, #22384 — created then immediately
  invalidated, fix unmerged). `cache_ram_mib` therefore buys nothing on Qwen3.8
  today and is worse than neutral, since admission charges it to commit in full
  and commit is the binding constraint. The RUNBOOK template omits the cache and
  checkpoint fields until the new agentic profile demonstrates real restoration.
- Agentic context-restoration benchmark (`qwen38-27b-iq4xs-agentic-restore`):
  60k base context grown by small increments interleaved with tool calls,
  recording prompt tokens processed against tokens new per turn. It is a
  regression gate — a turn after the first that reprocesses near the full context
  fails — and doubles as the acceptance test for whether a build fixed the
  upstream checkpoint defect.
- Evidence labels in `docs/BENCHMARKS.md` (`measured`, `modelled`,
  `upstream-reported`, `unverified on gfx1201`), and their application: the
  bandwidth and MTP tables are modelled, the kernel and `blk.64` figures are
  upstream-reported, and nothing about Qwen3.8-27B is measured here yet.
- `docs/ARCHITECTURE.md`: concurrency is enforced in four independent places and
  raising `CIA_EDGE_MAX_ACTIVE` alone does not make the system concurrent, it
  only converts queue waiting into upstream contention. Records what a real
  concurrency change would have to move together, and why the single-slot
  invariant is what the static VRAM budget depends on.
- Gate zero judges prefill as well as decode (`pp512`, `pp8192`, cold TTFT
  alongside `tg128`): a hybrid model can decode acceptably while prompt
  processing is severely degraded, which for a large standing context is the
  more expensive failure. Comparison is against saved baselines and the modelled
  ceiling rather than an invented threshold.
- `docs/RUNBOOK.md` §11.0: reclaim the idle commit baseline before measuring
  anything else. The 2026-07-20 validation recorded 31.82 GiB committed with no
  model loaded against a 42.30 GiB limit, which constrains a 27B more than the
  choice of quantization does.
- Hybrid Gated DeltaNet benchmark protocol in `docs/BENCHMARKS.md`, including the
  kernel go/no-go gate, the sweep matrix, and a cache-restoration measurement;
  matching `qwen38-27b-*` profiles in `model-test-matrix.json`.
- `docs/RUNBOOK.md` section 11: onboarding procedure for a partially offloaded
  hybrid model, and a capacity-`reason` interpretation table.

### Changed

- `--parallel` is now emitted from `model.parallel` instead of a hardcoded `1`.
  The schema still pins the value to `1`, so generated output is unchanged.
- `--context-shift` is no longer emitted unconditionally. Models with a recurrent
  state cannot be shifted, and a model that sets `context_shift: false` now gets
  an explicit `--no-context-shift`.
- Required commit for admission now includes `cache_ram_mib`. llama-server's
  host-RAM prompt cache is charged against the Windows commit limit, and the gate
  previously could not see it at all.
- The `canary_resource_measurement_pending` escape hatch no longer covers models
  that declare `tensor_overrides` or a non-zero `cache_ram_mib`; those fail closed
  with `resource_measurement_required_for_host_memory` until measured.
- `healthCheckTimeout` 180s to 600s and Codex `stream_idle_timeout_ms` 300s to
  900s. Both were sized for models that load and prefill entirely in VRAM.
- `systemCommitHeadroomGiB` becomes `systemMemoryStatus`, returning a
  `memorySnapshot` with both host memory axes; the `Server.commitHeadroom` hook
  becomes `Server.memoryStatus`. Signature change only — the injection point the
  tests use is unchanged.
- The `cache_ram_mib` starting value in the RUNBOOK §11.3 template drops from
  6144 to 2048. It was chosen against the nominal 10 GB RAM budget rather than
  against the 10.48 GiB of commit headroom actually measured, and the gate adds
  it in full on top of the model's own peak.
- `scripts/bench-llama.ps1` accepts `-RuntimeRoot`, `-NGpuLayers`, `-Label`, and
  `-RocBlasUseHipBlasLt`, and refuses `-UBatchSize` in the 65..256 band that
  collapses throughput on hybrid models.
- MTP draft depth for offloaded profiles drops from 5 to 3, in the RUNBOOK
  template and the `qwen38-27b-*` test-matrix profiles. 5 was carried over from
  resident-model guidance; speculation amortizes weight reads but not
  arithmetic, so a CPU-resident portion is compute-bound and each extra drafted
  token past a shallow depth costs more than it returns. `--threads` becomes a
  tuning variable for the same reason and is now part of the sweep matrix.

### Fixed

- Tray dashboard `Abrir Codex`/`Abrir OpenCode` only enabled for the model with
  the *persisted* selection (`Selecionar`) instead of whichever model was
  highlighted in the list; launch now targets the highlighted row.
- Codex canary launcher's console closed instantly on completion or error,
  hiding any output, because `powershell.exe` ran without `-NoExit`. Confirmed
  `codex.exe` is a genuine CLI that needs a real console (not a packaged-app
  shim) and restored `CREATE_NEW_CONSOLE` for it.

### Changed

- Removed `Abrir Codex` from the tray dashboard and tray context menu: the
  unified ChatGPT desktop app's bundled Codex mode has no GUI provider picker
  for custom/local providers (tracked upstream as `openai/codex#29156`); the
  CLI-only path remains available via `Start-CodexLocalCanary.ps1`.
- Registered the five non-Ornith canary models with validated
  `chat_completions` support (Qwen 3.5 ×3, Gemma 4 12B ×2) in the Codex and
  OpenCode local-only catalogs, with per-model `reasoning`/`tool_call`
  capability flags; left the two unvalidated Gemma 4 31B variants out
  (`chat_completions`/`streaming` not yet confirmed for them).
- Added the local provider to the personal default OpenCode configuration
  alongside existing cloud providers, previously reachable only through the
  isolated canary launcher; the OpenCode Start Menu shortcut now routes
  through the credential helper so the local provider authenticates without a
  standing plaintext environment variable.
- Raised `cia-mcp-inference` output-token ceiling (4096 → 65536 tokens) and
  the edge's upstream response timeout (2 min → 30 min), which were silently
  truncating and timing out ordinary delegate responses; added a configurable
  `temperature` (default 0.2, previously unset).
- Raised `local-coding` KV cache precision (`q4_0` → `q8_0`) and weight
  quantization (`Q4_K_M` → `Q8_0`, 9.53 GB); ~10.8 GiB VRAM measured with the
  full 131072-token context resident (within the operator's stated headroom).
- Lowered `local-coding` weight quantization again in the source manifest
  (`Q8_0` → `Q5_K_M`, 6.47 GB) to recover memory headroom, keeping KV cache at
  `q8_0`/`q8_0`; publication to the installed canary is staged and still
  awaits an elevated `New-V2Config.ps1 -Apply` run. llama.cpp's own
  device-memory fit dropped from 10764 MiB to 8210 MiB (~2.5 GiB recovered) at
  the full 131072-token context. A same-conditions A/B against the prior
  `Q8_0`/`q8_0` config (identical port, context, and quality-eval battery)
  showed no regression: 4/4 on the instruction-following/arithmetic/
  list-reasoning/config-recall suite with reasoning enabled, and a matching
  pass on a new long-context stress test (106745-token haystack, a fact
  planted at ~50% depth, correctly retrieved and reused as a forced
  tool-call argument).
- Removed the two never-validated Gemma 4 31B candidates (`gemma4-31b-q4km`,
  `gemma4-31b-ud-q4xl`; `chat_completions`/`streaming` had never been
  confirmed for either) and the superseded `Ornith 1.0 9B Q4_K_M` long-context
  test profiles from the manifest and test matrix after deleting their weight
  files (~39.8 GiB reclaimed); regenerated the Codex/OpenCode local-only
  catalogs from the cleaned manifest and updated canary validation/hash state
  to match.
- Documented the pinned executor model's demonstrated limitations and a
  capability rating directly in the `local_ai_delegate` tool description and
  `integrations/mcp-inference/README.md`, based on repeated canary testing.

### Security

- Removed cloud fallback and client-authorization forwarding from the v2 design.
- Separated inference, administration, and router credentials.
- Established metadata-only logging, fail-closed routing, loopback-only listeners, decompression limits, and explicit incident handling.
- Removed administrative credentials from status polling and default read-only
  MCP registrations; mutations read the admin secret only on demand.
- Added bounded switch completion after client cancellation, Explorer tray-icon
  recovery, idempotent panel startup, and rollback-safe elevated publication.
- Kept inference delegation isolated from the read-only and administrative MCP
  surfaces; endpoint/model are pinned, redirects/proxies are rejected, and no
  client configuration contains an inference credential.

### Migration

- v1 remains untouched and is not a v2 dependency.
- `local-coding` is a canary candidate only; final generation remains blocked until qualification.
- `local-fast` is recorded but disabled.
