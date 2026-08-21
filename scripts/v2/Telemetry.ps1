<#
.SYNOPSIS
Locale-independent host and GPU memory sampling for the v2 measurement scripts.

.DESCRIPTION
Dot-source this file; it defines functions and runs nothing.

Two problems motivate it.

The first is that `Get-Counter` paths are localized. `\Memory\Committed Bytes`
and `\Processor(_Total)\% Processor Time` do not resolve on a non-English
Windows, and the previous helper caught that failure and returned $null. A null
in a report is indistinguishable from "this run had no process to sample", so a
measurement that never happened read as a measurement that was not asked for.
Everything here reads CIM classes, whose names are invariant, or the GPU counter
set, whose English names are supplied by the display driver rather than by the
OS performance library and therefore do not localize.

The second is that per-process VRAM is the wrong instrument for this hardware.
`\GPU Process Memory(*)\Dedicated Usage` reports what one process was charged,
and it is accurate: measured against the adapter it agrees with the model's own
marginal cost to within 0.4%. What it cannot show is what the rest of the machine
is holding. This workstation's desktop and browser occupy roughly 3.0 GiB before
any model loads, so a process figure of 12.7 GiB on a 15.9 GiB card describes an
adapter at 97-98%, not at 80%. Past that point the AMD driver pages the excess to
system memory, prompt processing loses a factor of three, and no error is raised
(docs/TUNING.md section 1.6).

Adapter-level dedicated and shared usage are the only figures that show it, so
they are sampled here alongside the per-process ones rather than instead of them:
the process reading attributes cost, the adapter reading is what the budget
actually applies to.

Sampling is cheap but not free: a full adapter sample costs one counter read.
Callers drive the interval; nothing here polls on its own.
#>

# No Set-StrictMode here on purpose. This file is dot-sourced, and setting the
# mode would change it for the calling script rather than for these functions.
# Every function below is written to run under StrictMode -Version Latest,
# because Measure-V2AgenticReuse.ps1 sets it.

# Sampling an adapter that reports zero dedicated bytes tells us nothing and is
# usually a headless or disconnected LUID, so instance selection prefers the
# adapter actually holding memory. On this workstation three LUIDs are exposed
# and only the discrete GPU is ever non-zero.
function Get-V2AdapterMemorySample {
    <#
    .SYNOPSIS
    Adapter-level dedicated and shared GPU memory, in MiB.

    .DESCRIPTION
    Returns $null when the GPU counter set is unavailable, which is the honest
    result on a machine without a WDDM driver. Never returns a partial sample:
    dedicated and shared come from the same counter read so they describe the
    same instant.
    #>
    [CmdletBinding()]
    param()

    try {
        $samples = (Get-Counter -Counter '\GPU Adapter Memory(*)\Dedicated Usage', '\GPU Adapter Memory(*)\Shared Usage' -ErrorAction Stop).CounterSamples
    }
    catch {
        return $null
    }

    $dedicated = @($samples | Where-Object { $_.Path -like '*dedicated*' })
    if ($dedicated.Count -eq 0) { return $null }

    # The busiest adapter is the one under test. Summing across LUIDs would fold
    # an idle integrated GPU into the discrete GPU's figure and understate how
    # close the discrete adapter is to its own limit.
    $busiest = $dedicated | Sort-Object -Property CookedValue -Descending | Select-Object -First 1
    $instance = $busiest.InstanceName
    $sharedSample = @($samples | Where-Object { $_.Path -like '*shared*' -and $_.InstanceName -eq $instance })
    $sharedBytes = if ($sharedSample.Count -gt 0) { $sharedSample[0].CookedValue } else { 0 }

    return [pscustomobject]@{
        instance      = $instance
        dedicated_mib = [Math]::Round($busiest.CookedValue / 1MB, 1)
        shared_mib    = [Math]::Round($sharedBytes / 1MB, 1)
    }
}

function Get-V2ProcessVramMiB {
    <#
    .SYNOPSIS
    Dedicated GPU memory charged to one process, in MiB.

    .DESCRIPTION
    Kept because it attributes usage to a process, which the adapter sample
    cannot do. It is not a substitute for the adapter sample: this figure counts
    only what one process was charged, and the budget applies to the whole
    device, so it cannot say how close the adapter is to its limit.
    #>
    [CmdletBinding()]
    param([int]$ProcessId)

    if ($ProcessId -le 0) { return $null }
    try {
        $samples = (Get-Counter -Counter '\GPU Process Memory(*)\Dedicated Usage' -ErrorAction Stop).CounterSamples
    }
    catch {
        return $null
    }

    $total = 0
    $matched = $false
    foreach ($sample in $samples) {
        if ($sample.InstanceName -match "pid_$ProcessId(_|$)") {
            $total += $sample.CookedValue
            $matched = $true
        }
    }
    if (-not $matched) { return $null }
    return [Math]::Round($total / 1MB, 1)
}

function Get-V2HostMemorySample {
    <#
    .SYNOPSIS
    System-wide commit and physical memory, in GiB.

    .DESCRIPTION
    Win32_PerfRawData_PerfOS_Memory carries the same counters as the localized
    \Memory\* paths under an invariant class name, so this resolves identically
    on every Windows UI language. Physical figures come from
    Win32_OperatingSystem, which reports in kibibytes.

    commit_gib is what the edge admission gate reasons about; physical_used_gib
    is what decides whether CPU-resident weights stay in RAM instead of being
    paged to the SSD. They are different limits and both are reported.
    #>
    [CmdletBinding()]
    param()

    $commitGiB = $null
    $commitLimitGiB = $null
    try {
        $memory = Get-CimInstance -ClassName Win32_PerfRawData_PerfOS_Memory -ErrorAction Stop
        $commitGiB = [Math]::Round($memory.CommittedBytes / 1GB, 3)
        $commitLimitGiB = [Math]::Round($memory.CommitLimit / 1GB, 3)
    }
    catch {
        # Leave both null rather than substituting the OS view, which measures a
        # slightly different quantity and would make two runs incomparable.
    }

    $totalGiB = $null
    $availableGiB = $null
    try {
        $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
        $totalGiB = [Math]::Round($os.TotalVisibleMemorySize / 1MB, 3)
        $availableGiB = [Math]::Round($os.FreePhysicalMemory / 1MB, 3)
    }
    catch {
    }

    $usedGiB = $null
    if ($null -ne $totalGiB -and $null -ne $availableGiB) {
        $usedGiB = [Math]::Round($totalGiB - $availableGiB, 3)
    }

    return [pscustomobject]@{
        commit_gib           = $commitGiB
        commit_limit_gib     = $commitLimitGiB
        physical_total_gib   = $totalGiB
        physical_free_gib    = $availableGiB
        physical_used_gib    = $usedGiB
    }
}

function Get-V2ProcessMemorySample {
    <#
    .SYNOPSIS
    Working set and private commit for one process, in GiB.
    #>
    [CmdletBinding()]
    param([int]$ProcessId)

    if ($ProcessId -le 0) { return $null }
    try {
        $process = Get-Process -Id $ProcessId -ErrorAction Stop
    }
    catch {
        return $null
    }
    return [pscustomobject]@{
        working_set_gib = [Math]::Round($process.WorkingSet64 / 1GB, 3)
        private_gib     = [Math]::Round($process.PagedMemorySize64 / 1GB, 3)
    }
}

# Thresholds are derived from measured configurations on this workstation
# (benchmarks/REPORT-qwen38-27b-gfx1201-20260821.md), not chosen for roundness:
#
#   idle desktop        19.1% dedicated,  412 MiB shared   healthy
#   4-block @ 512 ctx   96.4% dedicated,  514 MiB shared   1034 t/s pp512, healthy
#   4-block @ 32k ctx   97.6% dedicated, 1253 MiB shared    195 t/s pp32768, degraded
#   full residency      98.6% dedicated, 1350 MiB shared    270 t/s pp512, degraded
#
# Occupancy alone does not separate these: the fastest configuration measured sits
# at 96.4%. Shared usage does, but only above the idle baseline - this desktop
# holds ~412 MiB of shared GPU memory with no model loaded at all, so a floor of a
# few hundred mebibytes would fire on an idle machine. The floor is therefore set
# between the healthy 514 MiB case and the degraded 1253 MiB one.
$script:V2VramOccupancyWarn = 0.95
$script:V2SharedFloorMiB = 1024

function Test-V2GpuMemoryPressure {
    <#
    .SYNOPSIS
    Classifies an adapter sample as ok, elevated, or pressured.

    .DESCRIPTION
    Diagnoses the degradation that has no other symptom. When the RX 9070 XT is
    driven past its dedicated budget the AMD driver pages the excess over PCIe
    instead of failing an allocation: nothing errors, llama.cpp's own accounting
    does not move, and prompt processing quietly loses a factor of three or more
    (docs/TUNING.md section 1.6).

    This reports; it never acts. Killing or resizing a model on a memory sample
    would be a policy decision taken from one instant of a noisy signal, and the
    operator is better placed to make it.

    -TotalMib is the adapter's dedicated budget. Pass the manifest's declared
    vram_mib rather than inferring one, so the verdict is measured against the
    same number admission control uses.
    #>
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]
        [AllowNull()]
        [object]$Sample,

        [Parameter(Mandatory = $true)]
        [ValidateRange(1, 1048576)]
        [int]$TotalMib
    )

    if ($null -eq $Sample) {
        return [pscustomobject]@{
            state = 'unknown'; occupancy = $null; shared_mib = $null
            message = 'GPU adapter counters are unavailable on this host.'
        }
    }

    $occupancy = $Sample.dedicated_mib / $TotalMib
    $shared = $Sample.shared_mib

    # Both conditions are required for 'pressured'. Shared usage on its own rises
    # with ordinary desktop compositing and says nothing about the model; high
    # occupancy on its own is the intended state for a configuration sized to the
    # card. Paging is what the pair identifies.
    $state = 'ok'
    if ($occupancy -ge $script:V2VramOccupancyWarn -and $shared -ge $script:V2SharedFloorMiB) {
        $state = 'pressured'
    }
    elseif ($occupancy -ge $script:V2VramOccupancyWarn) {
        $state = 'elevated'
    }

    $message = switch ($state) {
        'pressured' {
            @(
                'GPU memory pressure detected.',
                '',
                ('  Dedicated VRAM : {0:N0} / {1:N0} MiB ({2:P1})' -f $Sample.dedicated_mib, $TotalMib, $occupancy),
                ('  Shared GPU     : {0:N0} MiB' -f $shared),
                '',
                'Driver-level VRAM paging is likely. Prompt processing degrades',
                'without any error being raised. Consider a smaller context',
                'profile or a wider CPU tensor split.'
            ) -join [Environment]::NewLine
        }
        'elevated' {
            ('GPU memory is elevated: {0:N0} / {1:N0} MiB dedicated ({2:P1}), {3:N0} MiB shared.' -f $Sample.dedicated_mib, $TotalMib, $occupancy, $shared)
        }
        default {
            ('GPU memory is within budget: {0:N0} / {1:N0} MiB dedicated ({2:P1}), {3:N0} MiB shared.' -f $Sample.dedicated_mib, $TotalMib, $occupancy, $shared)
        }
    }

    return [pscustomobject]@{
        state      = $state
        occupancy  = [Math]::Round($occupancy, 4)
        shared_mib = $shared
        message    = $message
    }
}

function Get-V2MemorySample {
    <#
    .SYNOPSIS
    One combined host + adapter + process sample.

    .DESCRIPTION
    A single call so every field in a row describes the same instant. Callers
    that sample in a loop should use this rather than assembling the parts, or
    the columns drift apart under load.
    #>
    [CmdletBinding()]
    param([int]$ProcessId = 0)

    $adapter = Get-V2AdapterMemorySample
    $host_ = Get-V2HostMemorySample
    $process = Get-V2ProcessMemorySample -ProcessId $ProcessId

    return [pscustomobject]@{
        adapter_instance      = if ($null -ne $adapter) { $adapter.instance } else { $null }
        vram_dedicated_mib    = if ($null -ne $adapter) { $adapter.dedicated_mib } else { $null }
        vram_shared_mib       = if ($null -ne $adapter) { $adapter.shared_mib } else { $null }
        process_vram_mib      = Get-V2ProcessVramMiB -ProcessId $ProcessId
        process_ws_gib        = if ($null -ne $process) { $process.working_set_gib } else { $null }
        process_private_gib   = if ($null -ne $process) { $process.private_gib } else { $null }
        commit_gib            = $host_.commit_gib
        commit_limit_gib      = $host_.commit_limit_gib
        physical_used_gib     = $host_.physical_used_gib
        physical_free_gib     = $host_.physical_free_gib
        physical_total_gib    = $host_.physical_total_gib
    }
}
