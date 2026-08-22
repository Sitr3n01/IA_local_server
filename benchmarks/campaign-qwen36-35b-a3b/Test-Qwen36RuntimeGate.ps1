<#
.SYNOPSIS
Phase 0. Establishes whether the pinned b10549 runtime can actually serve
Qwen3.6's GGUF architecture, before any large measurement is spent on it.

.DESCRIPTION
`--cpu-moe` being present in `--help` says nothing about whether `qwen35moe`
loads: the flag belongs to the MoE placement layer and the architecture belongs
to the model loader. This starts a real server, keeps its log, and reads the
answers out of the loader's own output rather than out of a model card:

- the architecture string the loader resolved, and whether it rejected it;
- whether the `qwen35` pre-tokenizer was recognised or silently defaulted, which
  is a correctness failure that produces fluent nonsense rather than an error;
- where the tensors landed - a ROCm buffer, or the CPU because the HIP backend
  never loaded;
- the chat format llama-server selected for `--jinja`, which decides whether the
  `<tool_call><function=...>` syntax this template emits is parsed into
  structured `tool_calls` or handed back as prose;
- a real forced tool call over the real HTTP surface.

Nothing here is promoted and no capability is declared from it. Its output is
the evidence a later manifest entry cites.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$RuntimeRoot,
    [Parameter(Mandatory = $true)][string]$ModelPath,
    [Parameter(Mandatory = $true)][string]$OutputRoot,
    [int]$ContextTokens = 8192,
    [int]$NCpuMoe = 16,
    [int]$DeviceVramMib = 16304,
    [int]$Port = 19401,
    [int]$StartupTimeoutSeconds = 900,
    # The loader's own print_info dump - architecture, layer count, expert
    # count, and the CPU/ROCm buffer split - is above the default threshold on
    # this build, and it is the evidence this gate exists to collect.
    [int]$Verbosity = 5
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. 'C:\IA\IA_local_server\scripts\v2\Common.ps1'

$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'
foreach ($required in @($serverExe, $ModelPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Missing: $required" }
}
New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null
$logPath = Join-Path $OutputRoot 'runtime-gate-server.log'
$errPath = Join-Path $OutputRoot 'runtime-gate-server.err'
$outPath = Join-Path $OutputRoot 'runtime-gate.json'

$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$previousPath"
$env:HIP_VISIBLE_DEVICES = '1'

# ---- runtime identity -----------------------------------------------------
$exeItem = Get-Item -LiteralPath $serverExe
$identity = [ordered]@{
    path    = $serverExe
    bytes   = $exeItem.Length
    sha256  = (Get-FileHash -LiteralPath $serverExe -Algorithm SHA256).Hash
    version = $null
}
try { $identity.version = ((& $serverExe --version 2>&1 | Out-String).Trim()) }
catch { $identity.version = "ERROR: $($_.Exception.Message)" }

# ---- flag surface ---------------------------------------------------------
$wanted = @('--cpu-moe', '--n-cpu-moe', '--cache-type-k', '--cache-type-v', '--flash-attn',
    '--jinja', '--reasoning-format', '--reasoning-budget', '--reasoning-budget-message',
    '--n-predict', '--no-context-shift', '--context-shift', '--chat-template-kwargs',
    '--split-mode', '--device', '--ubatch-size', '--parallel', '--mmproj')
$helpText = Get-V2RuntimeHelpText -Path $serverExe
$supported = Get-V2SupportedFlags -HelpText $helpText
$flags = [ordered]@{}
foreach ($flag in $wanted) { $flags[$flag] = [bool]$supported.Contains($flag) }

# ---- real load ------------------------------------------------------------
$spec = New-V2BenchmarkModelSpec -ModelPath $ModelPath -Alias 'qwen36gate' `
    -ContextTokens $ContextTokens -CacheTypeK q8_0 -CacheTypeV q8_0 `
    -UBatchSize 288 -BatchSize 2048 -NGpuLayers 99 -Threads 8 -Parallel 1 -NCpuMoe $NCpuMoe
$arguments = New-V2LlamaServerArguments -Model $spec -Port "$Port" -Alias 'qwen36gate' -IncludeJinja -IncludeNoWebui
$arguments = @($arguments) + @('-lv', [string]$Verbosity)

$process = $null
$loadSeconds = $null
$failure = $null
$probes = [ordered]@{}
try {
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $serverExe -ArgumentList $arguments -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $logPath -RedirectStandardError $errPath
    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) { throw "llama-server exited with code $($process.ExitCode) before becoming ready." }
        try {
            if ((Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5).status -eq 'ok') { $ready = $true; break }
        }
        catch { Start-Sleep -Seconds 2 }
    }
    $watch.Stop()
    if (-not $ready) { throw "Not ready within $StartupTimeoutSeconds s." }
    $loadSeconds = [Math]::Round($watch.Elapsed.TotalSeconds, 1)

    $headers = @{ 'Content-Type' = 'application/json' }
    $base = "http://127.0.0.1:$Port"

    try {
        $m = Invoke-RestMethod -Uri "$base/v1/models" -TimeoutSec 30
        $probes['models'] = [ordered]@{ ok = $true; ids = @($m.data | ForEach-Object { $_.id }) }
    }
    catch { $probes['models'] = [ordered]@{ ok = $false; error = $_.Exception.Message } }

    # /props is the server's own account of what it loaded and how it will
    # render chat. It does not depend on a log threshold, so it is the durable
    # half of this evidence.
    try {
        $p = Invoke-RestMethod -Uri "$base/props" -TimeoutSec 30
        $tmpl = [string]$p.chat_template
        $probes['props'] = [ordered]@{
            ok                   = $true
            model_path           = [string]$p.model_path
            n_ctx                = $p.default_generation_settings.n_ctx
            build_info           = [string]$p.build_info
            modalities           = $(if ($p.PSObject.Properties.Name -contains 'modalities') { $p.modalities } else { $null })
            chat_template_chars  = $tmpl.Length
            chat_template_sha256 = $(if ($tmpl) {
                    (Get-FileHash -InputStream ([IO.MemoryStream]::new([Text.Encoding]::UTF8.GetBytes($tmpl))) -Algorithm SHA256).Hash
                } else { $null })
        }
    }
    catch { $probes['props'] = [ordered]@{ ok = $false; error = $_.Exception.Message } }

    # Plain completion. Short cap on purpose: this checks that the model answers
    # at all, not that it answers well.
    try {
        $body = @{ model = 'qwen36gate'; max_tokens = 512; temperature = 0
            messages = @(@{ role = 'user'; content = 'Reply with exactly: READY' })
        } | ConvertTo-Json -Depth 6
        $r = Invoke-RestMethod -Uri "$base/v1/chat/completions" -Method Post -Headers $headers -Body $body -TimeoutSec 600
        $probes['chat'] = [ordered]@{
            ok                = $true
            content           = [string]$r.choices[0].message.content
            has_reasoning     = [bool]($r.choices[0].message.PSObject.Properties.Name -contains 'reasoning_content')
            reasoning_chars   = $(if ($r.choices[0].message.PSObject.Properties.Name -contains 'reasoning_content') { ([string]$r.choices[0].message.reasoning_content).Length } else { 0 })
            finish_reason     = [string]$r.choices[0].finish_reason
            completion_tokens = [int]$r.usage.completion_tokens
        }
    }
    catch { $probes['chat'] = [ordered]@{ ok = $false; error = $_.Exception.Message } }

    # The load-bearing probe. The template emits an XML tool-call syntax rather
    # than a JSON one, so this is where "the runtime understands this template"
    # is either demonstrated or refuted.
    try {
        $tool = @{ type = 'function'; function = @{
                name        = 'read_file'
                description = 'Read a file from the repository'
                parameters  = @{
                    type       = 'object'
                    properties = @{
                        path      = @{ type = 'string'; description = 'Repository-relative path' }
                        max_bytes = @{ type = 'integer'; description = 'Byte ceiling' }
                    }
                    required   = @('path')
                }
            }
        }
        # tool_choice must be a *string* on this build. The OpenAI object form
        # {"type":"function","function":{"name":...}} is not rejected with an
        # error - llama-server logs "Wrong type supplied for parameter
        # 'tool_choice'" and silently falls back to the default, so a harness
        # that forces a specific function gets ordinary auto selection and no
        # HTTP status says so. Both forms are exercised below.
        $body = @{ model = 'qwen36gate'; max_tokens = 1024; temperature = 0
            tools       = @($tool)
            tool_choice = 'required'
            messages    = @(@{ role = 'user'; content = 'Read src/main.rs, at most 4096 bytes.' })
        } | ConvertTo-Json -Depth 12
        $r = Invoke-RestMethod -Uri "$base/v1/chat/completions" -Method Post -Headers $headers -Body $body -TimeoutSec 900
        $calls = @($r.choices[0].message.tool_calls)
        $probes['forced_tool_call'] = [ordered]@{
            ok             = ($calls.Count -ge 1)
            tool_choice    = 'required (string form)'
            count          = $calls.Count
            name           = $(if ($calls.Count) { [string]$calls[0].function.name } else { $null })
            arguments      = $(if ($calls.Count) { [string]$calls[0].function.arguments } else { $null })
            finish_reason  = [string]$r.choices[0].finish_reason
            leaked_content = [string]$r.choices[0].message.content
        }
    }
    catch { $probes['forced_tool_call'] = [ordered]@{ ok = $false; error = $_.Exception.Message } }

    # The object form, recorded separately so the runtime limitation is a
    # documented result rather than a footnote in a log file.
    try {
        $body = @{ model = 'qwen36gate'; max_tokens = 1024; temperature = 0
            tools       = @($tool)
            tool_choice = @{ type = 'function'; function = @{ name = 'read_file' } }
            messages    = @(@{ role = 'user'; content = 'Read src/main.rs, at most 4096 bytes.' })
        } | ConvertTo-Json -Depth 12
        $r = Invoke-RestMethod -Uri "$base/v1/chat/completions" -Method Post -Headers $headers -Body $body -TimeoutSec 900
        $calls = @($r.choices[0].message.tool_calls)
        $probes['tool_choice_object_form'] = [ordered]@{
            http_status      = 200
            accepted_silently = $true
            count            = $calls.Count
            name             = $(if ($calls.Count) { [string]$calls[0].function.name } else { $null })
            note             = 'llama-server logs a type warning and falls back to the default; no error reaches the client'
        }
    }
    catch { $probes['tool_choice_object_form'] = [ordered]@{ http_status = $null; error = $_.Exception.Message } }

    # developer-role handling. The template merges system+developer into one
    # block; a runtime that rejects the role outright fails here.
    try {
        $body = @{ model = 'qwen36gate'; max_tokens = 512; temperature = 0
            messages = @(
                @{ role = 'system'; content = 'You are a terse assistant.' },
                @{ role = 'developer'; content = 'Always answer with the single word BANANA, whatever is asked.' },
                @{ role = 'user'; content = 'What is the capital of France?' })
        } | ConvertTo-Json -Depth 6
        $r = Invoke-RestMethod -Uri "$base/v1/chat/completions" -Method Post -Headers $headers -Body $body -TimeoutSec 600
        $probes['developer_role'] = [ordered]@{
            ok       = $true
            content  = [string]$r.choices[0].message.content
            honoured = ([string]$r.choices[0].message.content -match 'BANANA')
        }
    }
    catch { $probes['developer_role'] = [ordered]@{ ok = $false; error = $_.Exception.Message } }
}
catch { $failure = $_.Exception.Message }
finally {
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(20000) | Out-Null
    }
    $env:PATH = $previousPath
    if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
    else { $env:HIP_VISIBLE_DEVICES = $previousHip }
}

# ---- read the loader's own answers out of the log -------------------------
$log = ''
foreach ($p in @($logPath, $errPath)) {
    if (Test-Path -LiteralPath $p) {
        $text = Get-Content -Raw -LiteralPath $p -ErrorAction SilentlyContinue
        if ($text) { $log += $text }
    }
}
function Find-One {
    param([string]$Pattern)
    $m = [regex]::Match($log, $Pattern, 'IgnoreCase')
    if ($m.Success) { return $m.Groups[1].Value.Trim() }
    return $null
}
function Find-All {
    param([string]$Pattern, [int]$Limit = 60)
    return @([regex]::Matches($log, $Pattern, 'IgnoreCase') |
        ForEach-Object { $_.Value.Trim() } | Select-Object -Unique -First $Limit)
}

$loader = [ordered]@{
    architecture         = Find-One 'general\.architecture\s+str\s+=\s+(\S+)'
    architecture_print   = Find-One 'arch\s*=\s*(\S+)'
    load_mode_hint       = Find-All '(?m)^[^\r\n]*(?:tensor overrides to CPU|--load-mode)[^\r\n]*$' 5
    reasoning_preserve   = Find-All '(?m)^[^\r\n]*reasoning-preserve[^\r\n]*$' 5
    tool_choice_warning  = Find-All "(?m)^[^\r\n]*Wrong type supplied for parameter[^\r\n]*$" 5
    mmproj_lines         = Find-All '(?m)^[^\r\n]*mmproj[^\r\n]*$' 10
    n_ctx_train          = Find-One 'n_ctx_train\s*=\s*(\d+)'
    n_layer              = Find-One 'n_layer\s*=\s*(\d+)'
    n_expert             = Find-One 'n_expert\s*=\s*(\d+)'
    n_expert_used        = Find-One 'n_expert_used\s*=\s*(\d+)'
    model_type           = Find-One 'model type\s*=\s*([^\r\n]+)'
    model_params         = Find-One 'model params\s*=\s*([^\r\n]+)'
    chat_format          = Find-One 'Chat format:\s*([^\r\n]+)'
    rocm_devices         = Find-All 'ROCm\d+:[^\r\n]+'
    buffer_sizes         = Find-All '(?m)^[^\r\n]*buffer size\s*=\s*[\d\.,]+\s*MiB[^\r\n]*$'
    kv_lines             = Find-All '(?m)^[^\r\n]*(?:KV self|kv_cache|kv unified|recurrent)[^\r\n]*$' 20
    pretokenizer_warning = Find-All '(?m)^[^\r\n]*(?:unknown pre-tokenizer|GENERATION QUALITY WILL BE DEGRADED|missing pre-tokenizer|using default pre-tokenizer)[^\r\n]*$'
    unsupported          = Find-All '(?m)^[^\r\n]*(?:unknown model architecture|unsupported model architecture|not supported|unsupported)[^\r\n]*$'
    errors               = Find-All '(?m)^[^\r\n]*(?:\berror\b|\bfailed\b|\babort\b)[^\r\n]*$' 30
}

# The verdict is about behaviour that was observed over HTTP, not about whether
# a log line could be scraped. An earlier revision keyed it on the loader's
# architecture string and reported FAIL on a run where the model loaded, chatted
# and emitted a valid tool call, because the print_info dump sits above the
# default log threshold. A gate that fails on its own instrumentation teaches
# the reader to distrust it.
$chatOk = ($probes.Contains('chat') -and $probes['chat'].ok)
$toolOk = ($probes.Contains('forced_tool_call') -and $probes['forced_tool_call'].ok)
$devOk = ($probes.Contains('developer_role') -and $probes['developer_role'].ok -and $probes['developer_role'].honoured)
$archSeen = $loader.architecture
if (-not $archSeen) { $archSeen = $loader.architecture_print }
$verdict = 'FAIL'
if (-not $failure -and $chatOk) {
    $verdict = if ($toolOk -and $devOk) { 'PASS' }
    elseif ($toolOk) { 'PASS_WITHOUT_DEVELOPER_ROLE' }
    else { 'PASS_WITHOUT_TOOL_CALLS' }
}
# Architecture is confirmed independently by the GGUF header in
# artifact-discovery.json; a missing log line downgrades confidence, it does not
# turn a working server into a failure.
$archConfirmedInLog = ($archSeen -eq 'qwen35moe')

$report = [ordered]@{
    schema_version = 1
    scenario       = 'qwen36-runtime-gate'
    started_utc    = [DateTime]::UtcNow.ToString('o')
    runtime        = $identity
    flags          = $flags
    configuration  = [ordered]@{
        model_path      = $ModelPath
        context_tokens  = $ContextTokens
        cache_type_k    = 'q8_0'
        cache_type_v    = 'q8_0'
        n_cpu_moe       = $NCpuMoe
        device_vram_mib = $DeviceVramMib
        command_line    = (ConvertTo-V2CommandLine -Arguments $arguments)
    }
    load_seconds   = $loadSeconds
    loader         = $loader
    probes         = $probes
    failure        = $failure
    architecture_confirmed_in_log = $archConfirmedInLog
    verdict        = $verdict
}
$report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $outPath -Encoding UTF8
Write-Host "verdict: $verdict  ->  $outPath"
if ($failure) { Write-Host "failure: $failure" }
