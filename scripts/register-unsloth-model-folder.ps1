param(
    [string]$ModelRoot = "C:\IA\unsloth-catalog",
    [string]$HostName = "127.0.0.1",
    [int]$Port = 8888,
    [string]$ApiKey = ""
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath $ModelRoot -PathType Container)) {
    throw "Model root not found: $ModelRoot"
}

if ([string]::IsNullOrWhiteSpace($ApiKey)) {
    $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $root = Split-Path -Parent $scriptDir
    $latestLog = Get-ChildItem -LiteralPath (Join-Path $root "logs") -Filter "unsloth-studio-*-stdout*.log" -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1
    if ($latestLog) {
        $line = Select-String -Path $latestLog.FullName -Pattern "API Key:" | Select-Object -Last 1
        if ($line) {
            $ApiKey = ($line.Line -replace "^.*API Key:\s*", "").Trim()
        }
    }
}

if ([string]::IsNullOrWhiteSpace($ApiKey)) {
    throw "ApiKey not provided and no Unsloth Studio API key was found in local logs."
}

$baseUrl = "http://$HostName`:$Port"
$headers = @{ Authorization = "Bearer $ApiKey" }
$body = @{ path = $ModelRoot } | ConvertTo-Json

try {
    $folder = Invoke-RestMethod -Method Post -Uri "$baseUrl/api/hub/scan-folders" -Headers $headers -ContentType "application/json" -Body $body -TimeoutSec 20
}
catch {
    throw "Failed to register $ModelRoot with Unsloth Studio at $baseUrl. $($_.Exception.Message)"
}

$local = Invoke-RestMethod -Method Get -Uri "$baseUrl/api/hub/local" -Headers $headers -TimeoutSec 30

[PSCustomObject]@{
    registered_folder = $folder
    model_count = @($local.models | Where-Object { $_.source -eq "custom" }).Count
    custom_models = @($local.models | Where-Object { $_.source -eq "custom" } | Select-Object display_name,path,format_variant)
}
