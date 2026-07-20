# Local llama.cpp Runtime

This directory contains a Windows ROCm llama.cpp runtime tuned for the local AMD Radeon RX 9070 XT.

## Layout

- `amd/`: AMD-validated ROCm 7.2.1 llama.cpp binaries.
- `models/`: legacy workspace model location. Current configured GGUF files live under `C:\IA\models`.
- `C:\IA\unsloth-catalog`: curated hardlink catalog registered in Unsloth Studio for a cleaner model picker.
- `scripts/`: launch and benchmark scripts.
- `logs/`: llama-server logs.
- `benchmarks/`: benchmark output.
- `mcp/`: minimal stdio MCP bridge that calls the local OpenAI-compatible server.
- `control/`: local browser panel and stdlib Python daemon for process control.

## Local control panel

Start the panel:

```powershell
.\work\local-llama\scripts\start-local-llama-panel.ps1
```

Open:

- Panel: `http://127.0.0.1:8090`
- llama.cpp Web UI, after starting a model: `http://127.0.0.1:8080`
- OpenAI-compatible API: `http://127.0.0.1:8080/v1`

The panel can start/stop `llama-server`, validate `/v1/models`, show logs, run a managed MCP self-test process, and display MCP context compaction state. The default panel profile is `ornith10-9b-q4km-kv-q4-128k` on the AMD runtime with `131072` context and `q4_0` KV cache. The server alias is `local-model`, so harnesses can use a stable model name even when the underlying GGUF profile changes. Panel-launched models also pass `--reasoning off --reasoning-budget 0` so MCP tools receive final-answer content by default.

## Windows tray startup

Windows Startup shortcuts are installed at:

```text
C:\Users\Sitr3n\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\Local Llama Model Tray.lnk
C:\Users\Sitr3n\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\Unsloth Studio Local.lnk
```

A quick tray launcher is also installed at:

```text
C:\Users\Sitr3n\Documents\Local Llama Model Tray.lnk
```

The tray launcher runs without loading a model by default:

```powershell
.\work\local-llama\scripts\local-llama-tray.ps1
```

The tray selector starts the control panel if needed and lets you switch profiles from the notification area. Startup intentionally does not load a model; the default Ornith 128k executor is loaded only when an MCP client asks for a model or when you start it from the tray/panel. Switching through the tray, panel, or MCP restarts the local executor with the same `local-model` alias, so MCP clients keep using the selected model without config changes.

Unsloth Studio is also started at login without loading a duplicate model. It uses the already registered `C:\IA\unsloth-catalog` folder and can call the `Local Llama Executor` MCP server registered in Studio.

The tray shortcuts use `assets\ineffa-tray.ico`, generated from the only image found in `Downloads` during setup. If a different PNG is the intended icon, rerun the installer with `-IconPath C:\Users\Sitr3n\Downloads\ineffa.png`.

To remove both startup entries:

```powershell
.\work\local-llama\scripts\uninstall-local-llama-startup.ps1
```

Configured model files:

- `C:\IA\models\Qwen3.5-9B-GGUF\Qwen3.5-9B-Q4_K_M.gguf`
- `C:\IA\models\Qwen3.5-9B-GGUF\Qwen3.5-9B-UD-Q4_K_XL.gguf`
- `C:\IA\models\gemma-4-12B-it-qat-GGUF\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf`
- `C:\IA\models\gemma-4-12B-it-qat-q4_0-gguf\gemma-4-12b-it-qat-q4_0.gguf`
- `C:\IA\models\Ornith-1.0-9B-GGUF\ornith-1.0-9b-Q4_K_M.gguf`

## Start the server

```powershell
.\work\local-llama\scripts\start-llama-server.ps1 -ModelPath .\work\local-llama\models\model.gguf
```

Defaults:

- Runtime: AMD validated binaries.
- API: `http://127.0.0.1:8080/v1`.
- GPU: the AMD runtime exposes the RX 9070 XT as `ROCm0`; the Unsloth fallback uses `HIP_VISIBLE_DEVICES=1`.
- Stability/performance guard: AMD runtime sets `ROCBLAS_USE_HIPBLASLT=0`; on this machine the AMD benchmark crashes in `libhipblaslt.dll` without it.
- llama.cpp flags: `--device ROCm0 --split-mode none -ngl 99 -fa on --cont-batching --warmup`.

Use the Unsloth build explicitly:

```powershell
.\work\local-llama\scripts\start-llama-server.ps1 -Runtime unsloth -ModelPath .\work\local-llama\models\model.gguf
```

## Unsloth Studio

Start Unsloth Studio with one of the configured local GGUF profiles:

```powershell
.\work\local-llama\scripts\start-unsloth-studio-profile.ps1 -ProfileId qwen35-9b-q4km-kv-q4-256k
```

Sync the curated model catalog and register it in the Studio "On Device" picker:

```powershell
.\work\local-llama\scripts\sync-unsloth-model-catalog.ps1 -Register -ReplaceScanFolders -Prune
```

Notes:

- `unsloth studio run` needs run-level options after `run`, e.g. `unsloth studio run --verbose ...`.
- The script uses the same model matrix as the local panel, so it loads GGUF files from `C:\IA\models`.
- The launcher sets `HF_HOME=C:\IA\hf-home`, so future Studio/Hugging Face downloads use `C:\IA` instead of filling the user profile cache.
- `C:\IA\unsloth-catalog` is registered as a custom scan folder in Studio; it uses hardlinks to the real GGUF files in `C:\IA\models`, so it does not duplicate model storage. Catalog names include the recommended runtime label, for example `AMD ROCm` or `Unsloth runtime`.
- `C:\IA\unsloth-catalog\00 CURRENT - local-model executor` is maintained by the control panel as metadata only. It intentionally does not contain a `CURRENT--...gguf` hardlink, because selecting it in the native Unsloth picker loads a second copy of the model.
- `C:\IA\models` is the raw storage location and should not be registered directly in Studio unless you want to see every downloaded file and cache-shaped folder.
- The Studio database has an enabled MCP entry named `Local Llama Executor`, pointing to `mcp\local-llama-mcp.py`. This lets Studio use the same selector tools as other harnesses.
- Selecting an arbitrary non-`CURRENT` GGUF directly inside the native Unsloth picker starts an Unsloth-owned flow. To switch the shared executor used by Codex, Claude Code, OpenCode, and other MCP clients, use the tray/panel or the `local_model_start_profile` MCP tool from inside Studio.
- Visual Studio Build Tools 2022 with the C++ workload and Windows SDK 10.0.26100.0 is installed on this machine; this fixes Triton/HIP startup compile errors such as missing `stdlib.h` or `basetsd.h`.
- Keep the local `llama-server` stopped before launching Studio if VRAM is tight.
- Verified profile: `qwen35-9b-q4km-kv-q4-256k` loads in Unsloth Studio at `http://127.0.0.1:8888/v1` with model id `Qwen3.5-9B-Q4_K_M`.

## Benchmark

```powershell
.\work\local-llama\scripts\bench-llama.ps1 -ModelPath .\work\local-llama\models\model.gguf
```

Compare AMD and Unsloth:

```powershell
.\work\local-llama\scripts\bench-llama.ps1 -Runtime amd -ModelPath .\work\local-llama\models\model.gguf
.\work\local-llama\scripts\bench-llama.ps1 -Runtime unsloth -ModelPath .\work\local-llama\models\model.gguf
```

## Device check

```powershell
.\work\local-llama\scripts\list-llama-devices.ps1 -Runtime amd
.\work\local-llama\scripts\list-llama-devices.ps1 -Runtime unsloth
```

## MCP bridge

Configure a local MCP client to run:

```powershell
python .\work\local-llama\mcp\local-llama-mcp.py
```

An example MCP client config is available at `mcp\mcp-config.example.json`.

Client-specific examples are available in `mcp\clients\`:

- `mcpServers.local-llama.json`: generic Claude Desktop, Cursor, Windsurf-style `mcpServers` JSON.
- `codex-local-llama.config.toml`: Codex `config.toml` snippet.
- `opencode.local-llama.jsonc`: OpenCode config snippet.
- `claude-code-add-local-llama.ps1`: Claude Code helper using `claude mcp add-json`.

The MCP bridge can autostart the panel and default model when a client calls `local_model_chat`, `local_model_models`, or `local_model_health`. Defaults:

- `LOCAL_LLAMA_BASE_URL=http://127.0.0.1:8080/v1`
- `LOCAL_LLAMA_PANEL_URL=http://127.0.0.1:8090`
- `LOCAL_LLAMA_MODEL=local-model`
- `LOCAL_LLAMA_DEFAULT_PROFILE=ornith10-9b-q4km-kv-q4-128k`
- `LOCAL_LLAMA_DEFAULT_RUNTIME=amd`
- `LOCAL_LLAMA_CONTEXT_LIMIT=131072`

The bridge exposes:

- `local_model_health`
- `local_model_models`
- `local_model_chat`
- `local_model_session_chat`
- `local_model_compact`
- `local_model_context_status`
- `local_model_profiles`
- `local_model_start_profile`
- `local_model_stop`

Session chat keeps MCP-side context by `session_id` and writes status to `mcp\context-state.json`, which the control panel reads for its context drawer. Automatic compaction triggers at 85% of the configured context limit by default and uses the active local model first.

Optional layer-2 SOTA compaction is disabled unless an API key is provided through environment variables:

```powershell
$env:LOCAL_LLAMA_SOTA_API_KEY = "<key>"
$env:LOCAL_LLAMA_SOTA_MODEL = "gpt-5"
```

Without a key, direct SOTA compaction fails clearly instead of sending data anywhere.

## Qwen/Gemma test matrix

The current local llama.cpp builds do not expose TurboQuant KV cache types such as `turbo4`; supported KV cache types are `f32`, `f16`, `bf16`, `q8_0`, `q4_0`, `q4_1`, `iq4_nl`, `q5_0`, and `q5_1`.

For the initial long-context tests, `q4_0` KV cache is used as the supported memory-saving mode:

- `qwen35-9b-ud-q4xl-kv-q4-256k`: AMD ROCm profile, `unsloth/Qwen3.5-9B-GGUF`, `Qwen3.5-9B-UD-Q4_K_XL.gguf`, 262144 context.
- `qwen35-9b-q4km-kv-q4-256k`: AMD ROCm profile, `unsloth/Qwen3.5-9B-GGUF`, `Qwen3.5-9B-Q4_K_M.gguf`, 262144 context.
- `gemma4-12b-qat-ud-q4xl-kv-q4-128k`: Unsloth runtime profile, `unsloth/gemma-4-12B-it-qat-GGUF`, `gemma-4-12B-it-qat-UD-Q4_K_XL.gguf`, 131072 context.
- `gemma4-12b-qat-q4_0-kv-q4-128k`: Unsloth runtime profile, `google/gemma-4-12B-it-qat-q4_0-gguf`, `gemma-4-12b-it-qat-q4_0.gguf`, 131072 context.
- `ornith10-9b-q4km-kv-q4-128k`: AMD ROCm profile, `deepreinforce-ai/Ornith-1.0-9B-GGUF`, `ornith-1.0-9b-Q4_K_M.gguf`, 131072 context.
- `ornith10-9b-q4km-kv-q4-256k`: AMD ROCm profile, `deepreinforce-ai/Ornith-1.0-9B-GGUF`, `ornith-1.0-9b-Q4_K_M.gguf`, 262144 context.

Download all test models:

```powershell
.\work\local-llama\scripts\download-test-models.ps1
```

Smoke test one profile:

```powershell
.\work\local-llama\scripts\run-profile-smoke.ps1 -ProfileId qwen35-9b-q4km-kv-q4-256k -Runtime amd
```

Endpoint benchmark one profile:

```powershell
.\work\local-llama\scripts\run-profile-chat-bench.ps1 -ProfileId qwen35-9b-q4km-kv-q4-256k -Runtime amd
```

Quality eval one profile:

```powershell
.\work\local-llama\scripts\run-profile-quality-eval.ps1 -ProfileId qwen35-9b-q4km-kv-q4-256k -Runtime amd
```

Runtime notes:

- Qwen3.5-9B runs on the AMD ROCm 7.2.1 runtime.
- Ornith 1.0 9B is Qwen-derived and is expected to run on the AMD ROCm 7.2.1 runtime.
- Gemma 4 fails on the AMD b8407 runtime with `unknown model architecture: 'gemma4'`; use the Unsloth fallback runtime for Gemma.
- Gemma 4 26B A4B was removed from active profiles after testing because its local performance was not good enough for the current harness target.
- Gemma 4 31B files may remain in `C:\IA\models` as raw storage until disk cleanup is explicitly requested, but they are no longer active profiles or part of the curated Studio catalog.
- July 19 eval snapshot: Ornith 1.0 9B passed 3/4 objective quality checks at 256k on AMD, missing one arithmetic prompt; Gemma 4 26B A4B passed 4/4 at 64k on Unsloth but was too slow for the current target.
