<#
.SYNOPSIS
Runs the serving-path contract probes against a direct llama-server.

.DESCRIPTION
This is the baseline leg of a three-way comparison. `edge_contract.py` takes a
base URL precisely so the same twelve probes run against a direct
`llama-server`, the llama-swap router, and the full edge; a difference between
the three is the finding, and one run against one of them proves nothing about
the others. The direct port has to go first, because a probe that fails here
is a runtime or model limitation and says nothing about the edge.

The profile served is the shipping `dedicated` IQ4_XS cell (32768 / q4_0 /
n_cpu_moe 16). IQ4_XS because it graded 10/10 on the Phase 4 rubric, and the
short context because cache size does not enter into what these probes test
and a small one loads faster.

Nothing here decides `capabilities.function_calling`. That flag stays false
until a forced tool call succeeds through the *edge*, which is a later leg.
#>
[CmdletBinding()]
param(
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$ModelPath = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF\Qwen3.6-35B-A3B-UD-IQ4_XS.gguf',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$Port = 19401,
    [int]$ContextTokens = 32768,
    [int]$NCpuMoe = 16,
    [int]$StartupTimeoutSeconds = 240
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

. 'C:\IA\IA_local_server\scripts\v2\Common.ps1'

$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'
foreach ($p in @($serverExe, $ModelPath)) {
    if (-not (Test-Path -LiteralPath $p)) { throw "Missing $p" }
}
$probe = 'C:\IA\IA_local_server\scripts\v2\eval\edge_contract.py'
if (-not (Test-Path -LiteralPath $probe)) { throw "Missing $probe" }

$stray = @(Get-Process -Name 'llama-server', 'llama-bench' -ErrorAction SilentlyContinue)
if ($stray.Count -gt 0) {
    throw ("{0} llama process(es) already running (pids {1})." -f `
            $stray.Count, (($stray | ForEach-Object { $_.Id }) -join ', '))
}

$logDir = Join-Path $OutputRoot 'contract'
if (-not (Test-Path -LiteralPath $logDir)) { New-Item -ItemType Directory -Path $logDir | Out-Null }
$logPath = Join-Path $logDir 'edge-contract-direct.log'
$errPath = Join-Path $logDir 'edge-contract-direct.err'
$outPath = Join-Path $logDir 'edge-contract-direct.json'

$spec = New-V2BenchmarkModelSpec -ModelPath $ModelPath -Alias 'qwen36contract' `
    -ContextTokens $ContextTokens -CacheTypeK q4_0 -CacheTypeV q4_0 `
    -UBatchSize 288 -BatchSize 2048 -NGpuLayers 99 -Threads 8 -Parallel 1 -NCpuMoe $NCpuMoe
$arguments = New-V2LlamaServerArguments -Model $spec -Port "$Port" -Alias 'qwen36contract' -IncludeJinja -IncludeNoWebui

# --device ROCm0 alone selects the iGPU on this host. See Enter-V2RocmEnvironment:
# the first revision of this script omitted it, the server loaded and served
# /v1/models, then aborted on the first completion with a rocBLAS error for
# gfx1036, and every probe recorded a connection failure as a model failure.
$rocm = Enter-V2RocmEnvironment -RuntimeRoot $RuntimeRoot

$process = $null
try {
    Write-Host "starting llama-server on port $Port ..."
    $process = Start-Process -FilePath $serverExe -ArgumentList $arguments -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $logPath -RedirectStandardError $errPath
    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) { throw "llama-server exited with code $($process.ExitCode) before becoming ready. See $errPath" }
        try {
            if ((Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5).status -eq 'ok') { $ready = $true; break }
        }
        catch { Start-Sleep -Milliseconds 750 }
    }
    if (-not $ready) { throw "llama-server did not become ready within $StartupTimeoutSeconds s. See $errPath" }
    Write-Host "ready. running probes ..."

    & python $probe --base-url "http://127.0.0.1:$Port" --alias qwen36contract `
        --label 'direct llama-server b10549, IQ4_XS ctx32768 q4_0 n_cpu_moe 16' --out $outPath
    Write-Host ("probe exit code: {0}" -f $LASTEXITCODE)
}
finally {
    Exit-V2RocmEnvironment -State $rocm
    if ($null -ne $process -and -not $process.HasExited) {
        Write-Host "stopping llama-server (pid $($process.Id))"
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(30000) | Out-Null
    }
}

if (Test-Path -LiteralPath $outPath) { Write-Host "wrote $outPath" }
