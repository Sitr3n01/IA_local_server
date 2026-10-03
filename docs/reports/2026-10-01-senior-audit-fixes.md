# Fixes from the 2026-10-01 technical audit

**Base:** commit `b698c4f278bf9191d61afc5021142d1eb7f14e3b`, branch `feat/harness-parallel-claude`. The changes were applied to the working tree after operator authorization. Nothing was committed or published, no installed binaries were replaced, and the runtime was not restarted.

All nine bug groups reproduced in the audit were fixed. The ten original reproductions passed against the changed code, and the scenarios were added to the suite with additional checks.

| ID | Fix | Main regression tests |
| --- | --- | --- |
| B01 | Text and tool indexes use the same sequence; deltas reuse the index of the correct block | `TestRegressionAnthropicMixedStreamIndexes`, `TestAnthropicStreamKeepsMultipleToolFragmentsOnTheirBlocks` |
| B02 | The OpenAI body is read before waiting for inference. Reservations capped at `MaxActive+MaxQueue` cover reading, queueing and execution; maintenance and control still refuse new admissions | `TestRegressionOpenAIQueuedBodySurvivesReadDeadline`, `TestDecodedBodyReservationsBoundRequestsBeforeReadingAndAreReleased`, existing queue and maintenance tests |
| B03 | The client context covers pipe reads and writes. Cancellation interrupts the synchronous I/O of the corresponding thread | `TestRegressionPipeExecuteHonorsExchangeTimeout`, `TestPipeClientCancellationInterruptsAnAdmittedExchange`, `TestPipeWriteDeadlineInterruptsAPeerThatDoesNotRead` |
| B04 | Write and flush have a separate 2-second budget and honor the server context. Close cancels pending I/O, waits for its workers and releases the handle | `TestRegressionPipeServeStopsWhenPeerDoesNotRead`, `TestPipeFlushHasADeadlineEvenWithoutServerCancellation`, `TestPipeCloseInterruptsABlockedReadWithoutAClientDeadline` |
| B05 | OpenAI routes check route, streaming, tools/history, structured output and reasoning capabilities before forwarding inference | `TestRegressionOpenAIRoutesEnforceCapabilities`, `TestCapabilitiesRefuseUnqualifiedFeaturesAndAllowPlainRequests` |
| B06 | EOF without completion and error/invalid chunks produce an SSE `error` event, without fabricating `end_turn` or `message_stop` | `TestRegressionAnthropicTruncatedStream`, `TestAnthropicStreamDistinguishesCompletionFromErrors` |
| B07 | Anthropic counts accept `usage` or `timings`. Fields received in separate chunks are preserved, and the final counts update the message | `TestRegressionAnthropicStreamUsageFromTimings`, `TestRegressionAnthropicNonstreamUsageFromTimings`, `TestAnthropicStreamingUsageMergesCountsAcrossChunks` |
| B08 | `tool_choice` translates `auto`, `none` and `any`, plus the parallelism option. Named selection is refused: there is evidence that the pinned runtime ignores it. Invalid policies and required tools on an unqualified model are also refused | `TestRegressionAnthropicEnforcesToolChoice`, `TestAnthropicToolChoiceTranslationAndInvalidPolicies`, `TestAnthropicRequiredToolChoiceRefusesAnUnqualifiedModel`, `TestNamedToolChoiceRefusesBeforeUpstream` |
| B09 | The monitor detects bodies larger than the limit and requires EOF after the JSON object. The same termination check was applied to the administrative framing | `TestActionRejectsMalformedBodies`, `TestServeConnRefusesMalformedOversizedAndUnknownRequests` |

**Compatibility consequences.**

Capabilities declared false are now enforced on the OpenAI routes. A request that requires one of them receives `400 unsupported_feature` before it reaches the runtime. This fix changed no qualification: no capability was promoted merely to keep previously accepted requests working. `responses` was later qualified for three models on recorded contract evidence; see the [readiness review](2026-10-01-deploy-readiness.md) and [`benchmarks/readiness-20261001`](../../benchmarks/readiness-20261001). The fixtures that exercise Responses were given explicit qualifications for their simulated backends, which preserves the memory and model-swap checks.

In the Anthropic adapter, a required tool choice is not downgraded to a response without tools. Optional tool offers still follow the existing policy for models without function calling. The input count, known only at the end of certain local streams, is sent in `message_delta`; the [current official SDK](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/lib/streaming/_messages.py) accepts this cumulative update.

On the pipe, the 10-second read deadline bounds receipt of the request and does not cancel an admitted operation that takes longer. `TestPipeReadDeadlineDoesNotCancelAnAdmittedOperation` covers this distinction. A client timeout can leave the outcome of an already admitted action uncertain; the transport does not retry the action. The cancellation implementation pins the thread for the duration of the syscall and waits for the cancellation worker before returning the thread to the Go runtime, which avoids cancelling I/O that belongs to other work. Reference: [CancelSynchronousIo](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-cancelsynchronousio).

**Unused code and documentation.**

`internal/modeloverlay/store.go` and its standalone tests, which had no production consumers, were removed, as was `ProfileFingerprint` in `internal/claudedesktop/manager.go`. The unused identity fields of the streaming state were also removed. Windows reachability analysis over `./cmd/... ./internal/...` reported no dead functions after the cleanup; that result holds for the roots and target analyzed, and is not a universal guarantee for every configuration.

The React console and its executables were kept, per ADR 0019. Configuration fields and launchers retained for compatibility were also kept. Both READMEs were brought in line with the current queue default and minimum Go version. Volatile counts of tests, scripts, ADRs and jobs were replaced with references to the sources and to the verification commands.

**Final validation.**

| Check | Result |
| --- | --- |
| Go on `./cmd/... ./internal/...`, without the test result cache | 703 tests/subtests passed; 18 packages with tests passed; 9 packages compiled with no tests; 6 conditional tests skipped |
| Ten original reproductions, via external overlay | All passed |
| Critical pipe timeout, cancellation, read, write and shutdown scenarios | 20 consecutive runs passed |
| `go vet ./...` | Passed |
| `staticcheck ./...` | Passed |
| `gofmt` on changed and new Go files | Nothing outstanding |
| `git diff --check` | No errors |
| Active monitor: DOM/jsdom | 11 tests passed; monitor lint passed |
| Build of the executables for Linux, `CGO_ENABLED=0` | Passed |
| Reachability of the project's functions on Windows | No findings after the cleanup |

The skipped tests depend on the E2E console, real memory consumers, an installed Claude instance, a C++ compiler, or installed tray tasks. Race detection was not run locally because no C compiler is available; the repeated pipe test is no substitute for the race detector. No model inference sessions, native desktop confirmation, or validation of the installed binaries were run at this stage. The Linux build confirms build portability, not that the Windows transport runs on another system.

**Reproducing the validation.**

```powershell
# Use the Go version from go.mod, in the repository directory.
go test ./cmd/... ./internal/... -count=1 -timeout=180s
go test ./internal/adminpipe -run '^Test(PipeClientCancellation|PipeWriteDeadline|PipeCloseInterrupts|PipeReadDeadline|RegressionPipeExecute|RegressionPipeServe)' -count=20 -timeout=45s
go vet ./...
staticcheck ./...

# In the frontend directory:
npm run test:monitor
npm run lint:monitor
```

Local evidence was saved to `C:\IA\tmp\audit-20261001-senior`: `fix-go-summary.json`, `fix-go.jsonl`, `fix-audit-reproductions.txt` and `fix-owned-deadcode-windows.json`. The original report remains as the record of the version before the fixes. The `.claude/` directory, already untracked before the audit, was left out of the changes.
