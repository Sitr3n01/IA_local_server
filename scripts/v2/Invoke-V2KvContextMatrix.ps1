<#
.SYNOPSIS
Prices a weights x KV-precision x context matrix at load, one llama-server start
per cell.

.DESCRIPTION
The question this answers is which (weights, KV, context) combinations can be
served at all on this adapter, and what each one costs, before any throughput or
quality work is spent on a cell that cannot fit. It is deliberately load-only:
`Measure-V2ContextFootprint.ps1` starts the server, waits for /health, samples
the adapter and the host, and stops, so a cell costs a model load rather than a
prefill of its whole window.

Cells are ordered cheapest-first within a model so that a failure appears at the
smallest context that produces it, and a model whose 32k cell already fails is
not carried to 256k. A cell that fails to load is recorded and the remaining
larger contexts for that KV pair are skipped rather than retried.

Emits one JSON file per cell plus a combined summary, so a partial run is still
evidence.

.EXAMPLE
./Invoke-V2KvContextMatrix.ps1 -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf `
    -Label iq4xs -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-256k
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ModelPath,

    [Parameter(Mandatory = $true)]
    [string]$Label,

    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',

    [int[]]$ContextTokens = @(32768, 65536, 131072, 262144),

    # Each entry is "K/V". The three the campaign compares are q8_0/q8_0,
    # q8_0/q4_0 and q4_0/q4_0.
    [string[]]$KvPairs = @('q8_0/q8_0', 'q8_0/q4_0', 'q4_0/q4_0'),

    [string]$TensorOverride = 'blk\.(6[0-3])\.ffn_.*=CPU',

    [ValidateRange(-1, 1024)]
    [int]$NCpuMoe = -1,

    [switch]$CpuMoe,

    [int]$UBatchSize = 288,

    [int]$Threads = 8,

    [int]$DeviceVramMib = 16304,

    [Parameter(Mandatory = $true)]
    [string]$OutputRoot,

    [ValidateRange(30, 3600)]
    [int]$StartupTimeoutSeconds = 900
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($CpuMoe -and $NCpuMoe -ge 0) {
    throw 'CpuMoe and NCpuMoe are mutually exclusive.'
}

$footprint = Join-Path $PSScriptRoot 'Measure-V2ContextFootprint.ps1'
if (-not (Test-Path -LiteralPath $footprint)) { throw "Missing $footprint" }
New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null

$rows = @()
foreach ($pair in $KvPairs) {
    $parts = $pair.Split('/')
    if ($parts.Count -ne 2) { throw "KvPairs entries must be 'K/V', got '$pair'" }
    $k = $parts[0]
    $v = $parts[1]
    $skipRest = $false

    foreach ($ctx in ($ContextTokens | Sort-Object)) {
        $cell = "{0}-ctx{1}-k{2}-v{3}" -f $Label, $ctx, $k, $v
        if ($skipRest) {
            Write-Host ("SKIP  {0} (a smaller context on this KV pair already failed)" -f $cell)
            $rows += [ordered]@{ cell = $cell; label = $Label; ctx = $ctx; k = $k; v = $v; status = 'skipped' }
            continue
        }

        $out = Join-Path $OutputRoot "footprint-$cell.json"
        Write-Host ("RUN   {0}" -f $cell)
        $report = & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $ModelPath `
            -ContextTokens $ctx -CacheTypeK $k -CacheTypeV $v `
            -TensorOverride $TensorOverride -UBatchSize $UBatchSize -Threads $Threads `
            -NCpuMoe $NCpuMoe -CpuMoe:$CpuMoe `
            -DeviceVramMib $DeviceVramMib -OutputPath $out `
            -StartupTimeoutSeconds $StartupTimeoutSeconds

        if ($report.failure) {
            Write-Warning ("FAIL  {0}: {1}" -f $cell, $report.failure)
            $rows += [ordered]@{ cell = $cell; label = $Label; ctx = $ctx; k = $k; v = $v; status = 'failed'; failure = $report.failure }
            $skipRest = $true
        }
        else {
            $rows += [ordered]@{
                cell               = $cell
                label              = $Label
                ctx                = $ctx
                k                  = $k
                v                  = $v
                status             = 'loaded'
                load_seconds       = $report.load_seconds
                idle_dedicated_mib = $report.idle.vram_dedicated_mib
                dedicated_mib      = $report.peak.vram_dedicated_mib
                shared_mib         = $report.peak.vram_shared_mib
                marginal_vram_mib  = $report.marginal_vram_mib
                process_ws_gib     = $report.peak.process_ws_gib
                process_private_gib = $report.peak.process_private_gib
                system_commit_gib  = $report.peak.commit_gib
                physical_used_gib  = $report.peak.physical_used_gib
                gpu_pressure       = $(if ($report.gpu_pressure) { $report.gpu_pressure.state } else { $null })
            }
        }

        # llama-server releases the adapter asynchronously; starting the next
        # cell too soon charges the previous model's pages to it.
        Start-Sleep -Seconds 6
    }
}

$summaryPath = Join-Path $OutputRoot "matrix-$Label.json"
[ordered]@{
    schema_version = 1
    scenario       = 'kv-context-matrix-at-load'
    label          = $Label
    model_path     = $ModelPath
    runtime_root   = $RuntimeRoot
    tensor_override = $TensorOverride
    cpu_moe        = [bool]$CpuMoe
    n_cpu_moe      = $(if ($NCpuMoe -ge 0) { $NCpuMoe } else { $null })
    ubatch_size    = $UBatchSize
    threads        = $Threads
    device_vram_mib = $DeviceVramMib
    started_utc    = [DateTime]::UtcNow.ToString('o')
    cells          = $rows
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $summaryPath -Encoding UTF8

Write-Host ""
Write-Host ("Summary -> {0}" -f $summaryPath)
$rows | ForEach-Object { [pscustomobject]$_ } | Format-Table -AutoSize cell, status, dedicated_mib, shared_mib, marginal_vram_mib, process_ws_gib, gpu_pressure
