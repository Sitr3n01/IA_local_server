<#
.SYNOPSIS
Phase 6. Prices the MTP head this GGUF ships: does `--spec-type draft-mtp`
decode faster than plain decode, and at what draft depth.

.DESCRIPTION
Ornith-1.5's GGUF carries a NextN/MTP block at `blk.40` - a full attention plus
256-expert FFN, 0.477 GB, quantized Q4_0 in every bartowski rung. Qwen3.6's
unsloth GGUFs carry no MTP tensors at all, so this is a dimension the Qwen3.6
campaign had no way to measure and the only axis on which these two models are
not comparing like with like.

Two things are worth separating, and this script keeps them separate:

- **Is the head usable at all?** `--spec-type draft-mtp` is accepted by the
  runtime's flag surface (Phase 0 records that), which says nothing about
  whether llama.cpp's MTP path is wired for `qwen35moe`. A server that starts
  and then drafts zero tokens is the answer to that, and it is recorded as such
  rather than as a throughput regression.
- **Is it worth its VRAM?** The head is loaded either way. At a fixed
  `--n-cpu-moe`, speculation that does not pay for itself means 0.477 GB of the
  budget bought nothing, and the matched-size comparison against Qwen3.6 should
  say so.

`draft_n_max = 0` is the control: the same server, same placement, same prompts,
with no `--spec-*` flag passed at all. Every other cell differs from it in
exactly one argument.

Acceptance rate is the number that explains the result, so it is read from the
server's own `timings` block per request rather than inferred from wall-clock.
Whatever key names the build uses are copied through verbatim - a field this
script does not recognise is still evidence.

.NOTES
Decode-bound by construction: short prompts, long generations, `--parallel 1`.
Speculation helps decode and does nothing for prefill, so a prompt-heavy mix
would dilute the very effect being measured.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ModelPath,
    [Parameter(Mandatory = $true)][string]$Label,
    [Parameter(Mandatory = $true)][string]$OutputRoot,
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [ValidateRange(1024, 1048576)][int]$ContextTokens = 32768,
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeK = 'q4_0',
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeV = 'q4_0',
    [ValidateRange(-1, 1024)][int]$NCpuMoe = -1,
    # 0 is the control: no --spec-* argument at all.
    [int[]]$DraftNMax = @(0, 2, 3, 5),
    # The MTP block owns 256 experts of its own. Leaving it on the GPU while the
    # main model's experts are on the CPU is a placement choice, not a default,
    # so it is a parameter. -1 means "do not pass the flag".
    [ValidateRange(-1, 1024)][int]$SpecDraftNCpuMoe = -1,
    [int]$UBatchSize = 288,
    [int]$BatchSize = 2048,
    [int]$NGpuLayers = 99,
    [int]$Threads = 8,
    [ValidateRange(1, 10)][int]$Repetitions = 3,
    [ValidateRange(16, 4096)][int]$NPredict = 256,
    [int]$DeviceVramMib = 16304,
    [ValidateRange(1024, 65535)][int]$Port = 19405,
    [ValidateRange(30, 3600)][int]$StartupTimeoutSeconds = 900,
    [int]$Seed = 20260824
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. 'C:\IA\IA_local_server\scripts\v2\Common.ps1'

$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'
foreach ($required in @($serverExe, $ModelPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Missing: $required" }
}
New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null

# Three domains on purpose. MTP acceptance is a property of how predictable the
# continuation is, and code, prose and JSON are not equally predictable. A single
# prompt would report one of these three numbers and call it "the" speedup.
$prompts = @(
    [ordered]@{
        id     = 'code'
        prompt = "Write a complete Go function `ParseDuration(s string) (time.Duration, error)` that accepts values like `1h30m`, `250ms` and `2h`, rejects anything else with a descriptive error, and is covered by a table-driven test. Output only code."
    },
    [ordered]@{
        id     = 'prose'
        prompt = 'Explain, in continuous prose and without lists, how a write-ahead log lets a database survive a crash midway through a transaction. Aim for about 300 words.'
    },
    [ordered]@{
        id     = 'json'
        prompt = 'Emit a JSON array of 12 objects. Each object has the keys "id" (integer, 1-12), "name" (a lowercase ascii identifier), "enabled" (boolean) and "weight" (a number between 0 and 1 with two decimals). Output only the JSON.'
    }
)

function Start-GateServer {
    param([string[]]$Arguments, [string]$LogPath, [string]$ErrPath)
    # Windows PowerShell 5.1's Start-Process does not quote array elements that
    # contain spaces before joining them into a command line. Pass one
    # pre-quoted string instead; ConvertTo-V2CommandLine is what builds the
    # display string in every other v2 script, so the two cannot drift.
    $argumentLine = ConvertTo-V2CommandLine -Arguments $Arguments
    return Start-Process -FilePath $serverExe -ArgumentList $argumentLine -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $LogPath -RedirectStandardError $ErrPath
}

$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$previousPath"
$env:HIP_VISIBLE_DEVICES = '1'

$cells = [System.Collections.Generic.List[object]]::new()

try {
    foreach ($draft in $DraftNMax) {
        $cellName = if ($draft -le 0) { 'off' } else { "n$draft" }
        $tag = "$Label-mtp-$cellName"
        Write-Host "=== MTP cell: $tag ==="

        $logPath = Join-Path $OutputRoot "server-$tag.log"
        $errPath = Join-Path $OutputRoot "server-$tag.log.err"

        $spec = New-V2BenchmarkModelSpec -ModelPath $ModelPath -Alias 'ornith15mtp' `
            -ContextTokens $ContextTokens -CacheTypeK $CacheTypeK -CacheTypeV $CacheTypeV `
            -UBatchSize $UBatchSize -BatchSize $BatchSize -NGpuLayers $NGpuLayers `
            -Threads $Threads -Parallel 1 -NCpuMoe $NCpuMoe
        $arguments = @(New-V2LlamaServerArguments -Model $spec -Port "$Port" -Alias 'ornith15mtp' -IncludeJinja -IncludeNoWebui)
        if ($draft -gt 0) {
            $arguments += @('--spec-type', 'draft-mtp', '--spec-draft-n-max', [string]$draft)
            if ($SpecDraftNCpuMoe -ge 0) {
                $arguments += @('--spec-draft-n-cpu-moe', [string]$SpecDraftNCpuMoe)
            }
        }

        $process = $null
        $failure = $null
        $loadSeconds = $null
        $runs = [System.Collections.Generic.List[object]]::new()
        try {
            $watch = [Diagnostics.Stopwatch]::StartNew()
            $process = Start-GateServer -Arguments $arguments -LogPath $logPath -ErrPath $errPath
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

            $base = "http://127.0.0.1:$Port"
            $headers = @{ 'Content-Type' = 'application/json' }

            # Warmup, discarded. The first generation after load pays for graph
            # allocation and would be charged to whichever cell ran first.
            $warm = @{ prompt = 'Say READY.'; n_predict = 16; temperature = 0; seed = $Seed; cache_prompt = $false } | ConvertTo-Json -Depth 4
            $null = Invoke-RestMethod -Uri "$base/completion" -Method Post -Headers $headers -Body $warm -TimeoutSec 600

            foreach ($p in $prompts) {
                for ($rep = 1; $rep -le $Repetitions; $rep++) {
                    $body = @{
                        prompt       = [string]$p.prompt
                        n_predict    = $NPredict
                        temperature  = 0
                        seed         = $Seed
                        # Prompt caching would let a later repetition skip
                        # prefill and report a decode rate the first one never
                        # saw. Every repetition pays the same price.
                        cache_prompt = $false
                    } | ConvertTo-Json -Depth 4
                    $reply = Invoke-RestMethod -Uri "$base/completion" -Method Post -Headers $headers -Body $body -TimeoutSec 3600
                    $timings = if ($reply.PSObject.Properties.Name -contains 'timings') { $reply.timings } else { $null }
                    $runs.Add([ordered]@{
                            prompt_id = [string]$p.id
                            repetition = $rep
                            # Copied through whole: field names differ between
                            # builds and an unrecognised key is still evidence.
                            timings = $timings
                            content_chars = ([string]$reply.content).Length
                            stop_type = $(if ($reply.PSObject.Properties.Name -contains 'stop_type') { [string]$reply.stop_type } else { $null })
                            tokens_predicted = $(if ($reply.PSObject.Properties.Name -contains 'tokens_predicted') { $reply.tokens_predicted } else { $null })
                        })
                }
            }
        }
        catch {
            $failure = $_.Exception.Message
            Write-Host "[fail] $tag : $failure"
        }
        finally {
            if ($process -and -not $process.HasExited) {
                Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
                Start-Sleep -Seconds 3
            }
        }

        # Whatever the build printed about drafting, kept verbatim. On a build
        # where the MTP path is not wired for this architecture, this is where
        # it says so.
        #
        # Both streams, and stderr first: llama-server writes its per-request
        # `slot print_timing` block - which is where `draft acceptance` lives -
        # to stderr and leaves stdout empty. Reading only stdout finds nothing
        # and would report it as "the head drafted nothing", which is the exact
        # opposite of what a working speculator looks like.
        $logText = ''
        foreach ($stream in @($errPath, $logPath)) {
            if (-not (Test-Path -LiteralPath $stream)) { continue }
            $raw = Get-Content -Raw -LiteralPath $stream
            # Get-Content -Raw on an empty file yields $null, not ''.
            if ($null -ne $raw) { $logText += $raw }
        }
        $specLines = @([regex]::Matches($logText, '(?m)^[^\r\n]*(?:draft acceptance|spec|nextn|mtp)[^\r\n]*$', 'IgnoreCase') |
            ForEach-Object { $_.Value.Trim() } | Select-Object -Unique -First 60)

        # The acceptance lines parsed into numbers, because this whole phase is a
        # comparison of them and a comparison of prose is not one. Kept next to
        # the raw lines rather than instead of them.
        $accepted = [System.Collections.Generic.List[object]]::new()
        $acceptPattern = 'draft acceptance\s*=\s*(?<rate>[\d\.]+)\s*\(\s*(?<acc>\d+)\s+accepted\s*/\s*(?<gen>\d+)\s+generated\s*\)[^\r\n]*mean len\s*=\s*(?<len>[\d\.]+)'
        foreach ($m in [regex]::Matches($logText, $acceptPattern)) {
            $accepted.Add([ordered]@{
                    rate      = [double]$m.Groups['rate'].Value
                    accepted  = [int]$m.Groups['acc'].Value
                    generated = [int]$m.Groups['gen'].Value
                    mean_len  = [double]$m.Groups['len'].Value
                })
        }

        # Decode rate as the server itself measured it, per request. The client
        # timings block carries the same figure; this one survives a build that
        # stops reporting timings over HTTP.
        $tgPattern = 'eval time\s*=\s*[\d\.]+\s*ms\s*/\s*\d+\s*tokens\s*\([^)]*?,\s*(?<tps>[\d\.]+)\s*tokens per second\)'
        $tgRates = @([regex]::Matches($logText, $tgPattern) |
            ForEach-Object { [double]$_.Groups['tps'].Value })

        $cells.Add([ordered]@{
                label            = $tag
                draft_n_max      = $draft
                speculation      = ($draft -gt 0)
                spec_draft_n_cpu_moe = $(if ($draft -gt 0 -and $SpecDraftNCpuMoe -ge 0) { $SpecDraftNCpuMoe } else { $null })
                command_line     = (ConvertTo-V2CommandLine -Arguments (@($serverExe) + $arguments))
                load_seconds     = $loadSeconds
                runs             = $runs
                draft_acceptance = $accepted
                draft_acceptance_mean = $(if ($accepted.Count -gt 0) {
                        [Math]::Round((($accepted | ForEach-Object { $_.rate } | Measure-Object -Average).Average), 5)
                    } else { $null })
                server_eval_tps  = $tgRates
                speculation_log_lines = $specLines
                failure          = $failure
            })
    }
}
finally {
    $env:PATH = $previousPath
    if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
    else { $env:HIP_VISIBLE_DEVICES = $previousHip }
}

$report = [ordered]@{
    schema_version = 1
    scenario       = 'ornith15-mtp-speculation'
    started_utc    = [DateTime]::UtcNow.ToString('o')
    runtime        = [ordered]@{
        path   = $serverExe
        sha256 = (Get-FileHash -LiteralPath $serverExe -Algorithm SHA256).Hash
    }
    configuration  = [ordered]@{
        model_path      = $ModelPath
        label           = $Label
        context_tokens  = $ContextTokens
        cache_type_k    = $CacheTypeK
        cache_type_v    = $CacheTypeV
        n_cpu_moe       = $NCpuMoe
        n_predict       = $NPredict
        repetitions     = $Repetitions
        seed            = $Seed
        device_vram_mib = $DeviceVramMib
    }
    cells          = $cells
}
$outPath = Join-Path $OutputRoot "mtp-$Label.json"
$report | ConvertTo-Json -Depth 14 | Set-Content -LiteralPath $outPath -Encoding UTF8
Write-Host "wrote $outPath"
