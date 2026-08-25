<#
Fills the last one-sided axis: decode with the window actually occupied,
measured on Qwen3.6 at the same profile classes and depths already measured on
Ornith. The Qwen3.6 campaign's Phase 5 never ran, so until now the strongest
speed number in the comparison had nothing to sit against.

Placements are Qwen3.6's own census minima per (context, cache) - the two models
do not need the same offload to hold the same window.

Deadline behaviour matches the retention drivers: a cell is only started if its
estimate fits, and what is skipped is logged by name.
#>
[CmdletBinding()]
param(
    [string]$CampaignRoot = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b',
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [int]$DeviceVramMib = 16304,
    [datetime]$Deadline = $(
        $t = [datetime]::Today.AddHours(11)
        if ($t -le (Get-Date)) { $t.AddDays(1) } else { $t }
    ),
    [switch]$WhatIf
)
$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$logPath = Join-Path $CampaignRoot 'qwen36-filled-driver.log'
$donePath = Join-Path $CampaignRoot 'qwen36-filled-driver.DONE'
Remove-Item -LiteralPath $donePath -Force -ErrorAction SilentlyContinue
function Write-Step { param([string]$m)
    $line = "[{0}] {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $m
    Write-Host $line
    for ($i=1; $i -le 5; $i++) {
        try { Add-Content -LiteralPath $logPath -Value $line -Encoding UTF8 -ErrorAction Stop; return }
        catch { Start-Sleep -Milliseconds (200*$i) }
    }
}

$artifacts = @{
    q2kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf'
    q3ks  = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'
    iq4xs = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf'
}
$throughput = 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ThroughputSweep.ps1'

$units = @(
    @{ m='q2kxl'; cls='deep';  ctx=32768;  kv='q8_0'; n=2;  tests=@('tg:128@30000');                est=12 }
    @{ m='q3ks';  cls='deep';  ctx=32768;  kv='q8_0'; n=12; tests=@('tg:128@30000');                est=15 }
    @{ m='iq4xs'; cls='deep';  ctx=32768;  kv='q8_0'; n=16; tests=@('tg:128@30000');                est=18 }
    @{ m='q2kxl'; cls='agent'; ctx=131072; kv='q4_0'; n=4;  tests=@('tg:128@32768','tg:128@120000'); est=25 }
    @{ m='q3ks';  cls='agent'; ctx=131072; kv='q4_0'; n=12; tests=@('tg:128@32768','tg:128@120000'); est=30 }
    @{ m='iq4xs'; cls='agent'; ctx=131072; kv='q4_0'; n=18; tests=@('tg:128@32768','tg:128@120000'); est=35 }
    @{ m='q2kxl'; cls='huge';  ctx=262144; kv='q4_0'; n=8;  tests=@('tg:128@131072','tg:128@240000'); est=45 }
    @{ m='q3ks';  cls='huge';  ctx=262144; kv='q4_0'; n=16; tests=@('tg:128@131072','tg:128@240000'); est=55 }
    @{ m='iq4xs'; cls='huge';  ctx=262144; kv='q4_0'; n=20; tests=@('tg:128@131072','tg:128@240000'); est=65 }
)

if (-not ('Native.FilledPowerRequest' -as [type])) {
    Add-Type -Namespace Native -Name FilledPowerRequest -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)]
public static extern uint SetThreadExecutionState(uint esFlags);
'@ -ErrorAction SilentlyContinue
}
$ES_CONTINUOUS = [uint32]'0x80000000'
$ES_SYSTEM_REQUIRED = [uint32]'0x00000001'
$awake = $false
$outRoot = Join-Path $CampaignRoot 'filled-qwen36'
New-Item -ItemType Directory -Force -Path $outRoot | Out-Null
$done = [System.Collections.Generic.List[string]]::new()
$skipped = [System.Collections.Generic.List[string]]::new()

try {
    if (-not $WhatIf) {
        $prev = [Native.FilledPowerRequest]::SetThreadExecutionState($ES_CONTINUOUS -bor $ES_SYSTEM_REQUIRED)
        if ($prev -ne 0) { $awake = $true }
    }
    Write-Step "=== Qwen3.6 filled-context decode; deadline $($Deadline.ToString('HH:mm')) ==="
    foreach ($u in $units) {
        $left = ($Deadline - (Get-Date)).TotalMinutes
        $label = "qwen36-{0}-filled-{1}-ctx{2}-ncpumoe{3}" -f $u.m, $u.cls, $u.ctx, $u.n
        if ($u.est -gt $left) {
            $skipped.Add("$label (needs ~$($u.est) min, $([int]$left) left)")
            Write-Step "SKIP $label : does not fit in $([int]$left) min"
            continue
        }
        if ($WhatIf) { Write-Step ("PLAN {0} est {1} min ({2} left)" -f $label, $u.est, [int]$left); continue }
        Write-Step ("--- begin {0} | {1} min to deadline" -f $label, [int]$left)
        $w = [Diagnostics.Stopwatch]::StartNew()
        & $throughput -RuntimeRoot $RuntimeRoot -ModelPath $artifacts[$u.m] -Label $label `
            -ContextTokens $u.ctx -CacheTypeK $u.kv -CacheTypeV $u.kv -NCpuMoe $u.n `
            -Repetitions 2 -Tests ([string[]]$u.tests) -DeviceVramMib $DeviceVramMib -OutputRoot $outRoot 2>&1 |
            Tee-Object -FilePath (Join-Path $outRoot ("run-{0}.log" -f $label)) | Out-Null
        $w.Stop()
        $done.Add($label)
        Write-Step ("--- end {0} after {1:N1} min" -f $label, $w.Elapsed.TotalMinutes)
    }
}
finally {
    if ($awake) { try { [void][Native.FilledPowerRequest]::SetThreadExecutionState($ES_CONTINUOUS) } catch {} ; Write-Step 'sleep suppression released' }
    $summary = "done={0} skipped={1}" -f $done.Count, $skipped.Count
    Write-Step "=== $summary ==="
    foreach ($s in $skipped) { Write-Step "  not run: $s" }
    if (-not $WhatIf) { Set-Content -LiteralPath $donePath -Encoding UTF8 -Value (@($summary; 'completed:'; $done; 'not run:'; $skipped) -join [Environment]::NewLine) }
}
