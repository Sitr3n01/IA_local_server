<#
.SYNOPSIS
A/B for `--load-mode` on a configuration with expert weights on the host.

.DESCRIPTION
The runtime asks for this itself. Loading Qwen3.6 with `--n-cpu-moe` emits:

    tensor overrides to CPU are used with mmap enabled - consider using
    --load-mode none for better performance

Under `auto` the host-resident experts are `CPU_Mapped`: file-backed pages that
cost no commit, which is why §7.4 of the report measures commit equal to VRAM
while several GiB of expert weight sits in system memory. Under `none` they are
read into ordinary process memory instead. That is a real trade, not a free
speed-up - it converts file-backed pages into private commit, and commit is the
binding constraint on this workstation - so it needs both halves measured before
anything is decided.

Deliberately outside the manifest. `load_mode` is not a schema field and this
does not add one: the campaign's order is measure, then encode evidence, then
promote. If `none` wins by enough to matter, the field is worth proposing; if it
does not, the repository is spared a knob that only ever carries its default.

Throughput comes from llama-bench (which has its own `-lm`), and the memory
figures come from a llama-server load of the same configuration, because
llama-bench reports neither commit nor working set.
#>
[CmdletBinding()]
param(
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$ModelPath = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF\Qwen3.6-35B-A3B-UD-Q3_K_XL.gguf',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$NCpuMoe = 12,
    [string[]]$Modes = @('auto', 'none'),
    [int[]]$PromptLengths = @(512, 8192),
    [int]$GenTokens = 128,
    [int]$Repetitions = 3,
    [int]$DeviceVramMib = 16304,
    [int]$Port = 19402
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path 'C:\IA\IA_local_server\scripts\v2' 'Telemetry.ps1')
. (Join-Path 'C:\IA\IA_local_server\scripts\v2' 'Common.ps1')

$benchExe = Join-Path $RuntimeRoot 'llama-bench.exe'
$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'
foreach ($required in @($benchExe, $serverExe, $ModelPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Missing: $required" }
}
$outDir = Join-Path $OutputRoot 'loadmode'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$previousPath"
$env:HIP_VISIBLE_DEVICES = '1'

$rows = [System.Collections.Generic.List[object]]::new()
try {
    foreach ($mode in $Modes) {
        # --- throughput -----------------------------------------------------
        foreach ($tokens in $PromptLengths) {
            foreach ($kind in @('pp', 'tg')) {
                if ($kind -eq 'tg' -and $tokens -ne $PromptLengths[0]) { continue }
                $n = if ($kind -eq 'pp') { $tokens } else { $GenTokens }
                $tag = "loadmode-$mode-ncpumoe$NCpuMoe-$kind$n"
                $stdout = Join-Path $outDir "bench-$tag.json"
                $stderr = Join-Path $outDir "bench-$tag.err"
                $benchArgs = @('-m', $ModelPath, '-dev', 'ROCm0', '--split-mode', 'none',
                    '-ngl', '99', '-fa', 'on', '-b', '2048', '-ub', '288',
                    '-ctk', 'q4_0', '-ctv', 'q4_0', '-t', '8',
                    '-r', [string]$Repetitions, '-o', 'json',
                    '--n-cpu-moe', [string]$NCpuMoe, '-lm', $mode)
                if ($kind -eq 'pp') { $benchArgs += @('-p', [string]$n, '-n', '0') }
                else { $benchArgs += @('-p', '0', '-n', [string]$n) }

                Write-Host "RUN   $tag"
                $watch = [Diagnostics.Stopwatch]::StartNew()
                $proc = Start-Process -FilePath $benchExe -ArgumentList $benchArgs -PassThru -WindowStyle Hidden `
                    -RedirectStandardOutput $stdout -RedirectStandardError $stderr
                $proc.WaitForExit(3600000) | Out-Null
                $watch.Stop()

                $value = $null; $stddev = $null
                try {
                    $parsed = Get-Content -Raw -LiteralPath $stdout | ConvertFrom-Json
                    $value = [Math]::Round([double]$parsed[0].avg_ts, 2)
                    $stddev = [Math]::Round([double]$parsed[0].stddev_ts, 2)
                }
                catch { }
                $rows.Add([ordered]@{ mode = $mode; measurement = "$kind$n"; tokens_per_second = $value
                        stddev = $stddev; wall_s = [Math]::Round($watch.Elapsed.TotalSeconds, 1) })
                Write-Host ("      {0,9:N2} t/s  ({1:N0}s)" -f $value, $watch.Elapsed.TotalSeconds)
            }
        }

        # --- memory, from a server load of the same configuration ------------
        $idle = Get-V2MemorySample -ProcessId 0
        $spec = New-V2BenchmarkModelSpec -ModelPath $ModelPath -Alias 'loadmode' -ContextTokens 32768 `
            -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -BatchSize 2048 `
            -NGpuLayers 99 -Threads 8 -Parallel 1 -NCpuMoe $NCpuMoe
        $serverArgs = @(New-V2LlamaServerArguments -Model $spec -Port "$Port" -Alias 'loadmode' -IncludeNoWebui) + @('-lm', $mode)
        $srvLog = Join-Path $outDir "server-loadmode-$mode.log"
        $srvErr = Join-Path $outDir "server-loadmode-$mode.err"

        Write-Host "LOAD  server -lm $mode"
        $watch = [Diagnostics.Stopwatch]::StartNew()
        $srv = Start-Process -FilePath $serverExe -ArgumentList $serverArgs -PassThru -WindowStyle Hidden `
            -RedirectStandardOutput $srvLog -RedirectStandardError $srvErr
        $deadline = [DateTime]::UtcNow.AddSeconds(900)
        $ready = $false
        while ([DateTime]::UtcNow -lt $deadline) {
            if ($srv.HasExited) { break }
            try { if ((Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5).status -eq 'ok') { $ready = $true; break } }
            catch { Start-Sleep -Seconds 2 }
        }
        $watch.Stop()
        $peak = $null
        if ($ready) {
            $samples = @()
            for ($i = 0; $i -lt 4; $i++) { Start-Sleep -Milliseconds 700; $samples += Get-V2MemorySample -ProcessId $srv.Id }
            $peak = [ordered]@{
                vram_dedicated_mib  = ($samples | ForEach-Object { $_.vram_dedicated_mib } | Measure-Object -Maximum).Maximum
                vram_shared_mib     = ($samples | ForEach-Object { $_.vram_shared_mib } | Measure-Object -Maximum).Maximum
                process_private_gib = ($samples | ForEach-Object { $_.process_private_gib } | Measure-Object -Maximum).Maximum
                process_ws_gib      = ($samples | ForEach-Object { $_.process_ws_gib } | Measure-Object -Maximum).Maximum
            }
        }
        if (-not $srv.HasExited) { Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue; $srv.WaitForExit(20000) | Out-Null }

        $bufferLines = @()
        if (Test-Path -LiteralPath $srvErr) {
            $text = Get-Content -Raw -LiteralPath $srvErr
            $bufferLines = @([regex]::Matches($text, '(?m)^[^\r\n]*model buffer size[^\r\n]*$') |
                ForEach-Object { $_.Value.Trim() } | Select-Object -Unique)
        }
        $rows.Add([ordered]@{ mode = $mode; measurement = 'server-load'
                load_seconds = [Math]::Round($watch.Elapsed.TotalSeconds, 1)
                ready = $ready; idle_vram_mib = $idle.vram_dedicated_mib
                peak = $peak; model_buffers = $bufferLines })
    }
}
finally {
    $env:PATH = $previousPath
    if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
    else { $env:HIP_VISIBLE_DEVICES = $previousHip }
}

$outPath = Join-Path $outDir 'loadmode-ab.json'
[ordered]@{
    schema_version = 1
    scenario       = 'load-mode-ab'
    started_utc    = [DateTime]::UtcNow.ToString('o')
    configuration  = [ordered]@{
        model_path = $ModelPath; runtime_root = $RuntimeRoot; n_cpu_moe = $NCpuMoe
        cache_type_k = 'q4_0'; cache_type_v = 'q4_0'; ubatch_size = 288; batch_size = 2048
        threads = 8; repetitions = $Repetitions; device_vram_mib = $DeviceVramMib
    }
    results        = $rows
} | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $outPath -Encoding UTF8

Write-Host ''
Write-Host "Summary -> $outPath"
$rows | Where-Object { $_.measurement -ne 'server-load' } |
    ForEach-Object { [pscustomobject]$_ } | Format-Table -AutoSize mode, measurement, tokens_per_second, stddev, wall_s
