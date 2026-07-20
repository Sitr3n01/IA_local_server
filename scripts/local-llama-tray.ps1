param(
    [string]$DefaultProfileId = "ornith10-9b-q4km-kv-q4-128k",
    [switch]$StartDefault,
    [string]$PanelUrl = "http://127.0.0.1:8090",
    [string]$IconPath = ""
)

$ErrorActionPreference = "Stop"

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
$matrixPath = Join-Path $root "model-test-matrix.json"
$panelScript = Join-Path $scriptDir "start-local-llama-panel.ps1"

function Invoke-PanelApi {
    param(
        [string]$Method,
        [string]$Path,
        [object]$Body = $null,
        [int]$TimeoutSec = 10
    )

    $uri = "$PanelUrl$Path"
    if ($null -eq $Body) {
        return Invoke-RestMethod -Method $Method -Uri $uri -TimeoutSec $TimeoutSec
    }

    $json = $Body | ConvertTo-Json -Depth 8
    return Invoke-RestMethod -Method $Method -Uri $uri -ContentType "application/json" -Body $json -TimeoutSec $TimeoutSec
}

function Ensure-Panel {
    try {
        Invoke-PanelApi -Method Get -Path "/api/status" -TimeoutSec 3 | Out-Null
        return
    }
    catch {
        Start-Process -FilePath powershell -ArgumentList @(
            "-NoProfile",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            $panelScript
        ) -WindowStyle Hidden | Out-Null
    }

    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Seconds 1
        try {
            Invoke-PanelApi -Method Get -Path "/api/status" -TimeoutSec 3 | Out-Null
            return
        }
        catch {}
    }

    throw "Local llama panel did not start at $PanelUrl"
}

function Show-Balloon {
    param(
        [System.Windows.Forms.NotifyIcon]$NotifyIcon,
        [string]$Title,
        [string]$Text,
        [System.Windows.Forms.ToolTipIcon]$Icon = [System.Windows.Forms.ToolTipIcon]::Info
    )

    $NotifyIcon.BalloonTipTitle = $Title
    $NotifyIcon.BalloonTipText = $Text
    $NotifyIcon.BalloonTipIcon = $Icon
    $NotifyIcon.ShowBalloonTip(2500)
}

function Get-TrayIcon {
    param([string]$Path)

    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) {
        return [System.Drawing.SystemIcons]::Application
    }

    $resolved = (Resolve-Path -LiteralPath $Path).Path
    if ([System.IO.Path]::GetExtension($resolved).ToLowerInvariant() -eq ".ico") {
        return New-Object System.Drawing.Icon($resolved)
    }

    $script:trayBitmap = [System.Drawing.Bitmap]::FromFile($resolved)
    $handle = $script:trayBitmap.GetHicon()
    return [System.Drawing.Icon]::FromHandle($handle)
}

function Get-StatusText {
    try {
        $status = Invoke-PanelApi -Method Get -Path "/api/status" -TimeoutSec 3
        $tokens = $status.tokens
        $ctxText = ""
        if ($tokens -and $null -ne $tokens.estimated_context_percent) {
            $ctxText = " | ctx $($tokens.estimated_context_percent)%"
            if ($null -ne $tokens.requests_processing) {
                $ctxText += " | req $($tokens.requests_processing)"
            }
            if ($tokens.tokens_per_second) {
                $ctxText += " | $($tokens.tokens_per_second) tok/s"
            }
        }
        if ($status.model.alive -and $status.model.active.profile_id) {
            return "Active: $($status.model.active.profile_id)$ctxText"
        }
        if ($status.current_profile.profile_id) {
            return "Selected: $($status.current_profile.profile_id)$ctxText"
        }
        return "No model active"
    }
    catch {
        return "Panel offline"
    }
}

function Start-Profile {
    param(
        [string]$ProfileId,
        [System.Windows.Forms.NotifyIcon]$NotifyIcon
    )

    try {
        Ensure-Panel
        $body = @{
            profile_id = $ProfileId
            alias = "local-model"
        }
        Invoke-PanelApi -Method Post -Path "/api/model/start" -Body $body -TimeoutSec 20 | Out-Null
        Show-Balloon -NotifyIcon $NotifyIcon -Title "Local model" -Text "Started $ProfileId"
    }
    catch {
        Show-Balloon -NotifyIcon $NotifyIcon -Title "Local model error" -Text $_.Exception.Message -Icon Error
    }
}

function Stop-Model {
    param([System.Windows.Forms.NotifyIcon]$NotifyIcon)

    try {
        Ensure-Panel
        Invoke-PanelApi -Method Post -Path "/api/model/stop" -Body @{} -TimeoutSec 10 | Out-Null
        Show-Balloon -NotifyIcon $NotifyIcon -Title "Local model" -Text "Stopped model"
    }
    catch {
        Show-Balloon -NotifyIcon $NotifyIcon -Title "Local model error" -Text $_.Exception.Message -Icon Error
    }
}

$matrix = Get-Content -Raw -LiteralPath $matrixPath | ConvertFrom-Json
$profiles = @($matrix.profiles)

$notify = New-Object System.Windows.Forms.NotifyIcon
$notify.Icon = Get-TrayIcon -Path $IconPath
$notify.Text = "Local Llama model selector"
$notify.Visible = $true

$menu = New-Object System.Windows.Forms.ContextMenuStrip

$statusItem = New-Object System.Windows.Forms.ToolStripMenuItem("Status: starting")
$statusItem.Enabled = $false
[void]$menu.Items.Add($statusItem)
[void]$menu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

$defaultItem = New-Object System.Windows.Forms.ToolStripMenuItem("Start default Ornith 128k")
$defaultItem.Tag = $DefaultProfileId
$defaultItem.add_Click({ Start-Profile -ProfileId ([string]$this.Tag) -NotifyIcon $notify })
[void]$menu.Items.Add($defaultItem)
[void]$menu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

foreach ($profile in $profiles) {
    $item = New-Object System.Windows.Forms.ToolStripMenuItem([string]$profile.display_name)
    $item.Tag = [string]$profile.id
    $item.add_Click({ Start-Profile -ProfileId ([string]$this.Tag) -NotifyIcon $notify })
    [void]$menu.Items.Add($item)
}

[void]$menu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

$openPanelItem = New-Object System.Windows.Forms.ToolStripMenuItem("Open control panel")
$openPanelItem.add_Click({ Start-Process $PanelUrl })
[void]$menu.Items.Add($openPanelItem)

$openServerItem = New-Object System.Windows.Forms.ToolStripMenuItem("Open llama.cpp UI")
$openServerItem.add_Click({ Start-Process "http://127.0.0.1:8080" })
[void]$menu.Items.Add($openServerItem)

$stopItem = New-Object System.Windows.Forms.ToolStripMenuItem("Stop model")
$stopItem.add_Click({ Stop-Model -NotifyIcon $notify })
[void]$menu.Items.Add($stopItem)

[void]$menu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

$exitItem = New-Object System.Windows.Forms.ToolStripMenuItem("Exit tray")
$exitItem.add_Click({
    $notify.Visible = $false
    $notify.Dispose()
    [System.Windows.Forms.Application]::Exit()
})
[void]$menu.Items.Add($exitItem)

$notify.ContextMenuStrip = $menu
$notify.add_DoubleClick({ Start-Process $PanelUrl })
$menu.add_Opening({
    $statusItem.Text = Get-StatusText
})

if ($StartDefault) {
    Start-Profile -ProfileId $DefaultProfileId -NotifyIcon $notify
}
else {
    Ensure-Panel
}

[System.Windows.Forms.Application]::Run()
