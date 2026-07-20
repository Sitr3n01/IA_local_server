param(
    [string]$HfHubPath = "C:\IA\hf-home\hub",
    [string]$CacheName = "models--unsloth--Qwen3.5-4B-MTP-GGUF"
)

$ErrorActionPreference = "Stop"

$hub = Resolve-Path -LiteralPath $HfHubPath -ErrorAction Stop
$targets = @(
    (Join-Path $hub.Path $CacheName),
    (Join-Path $hub.Path ".locks\$CacheName")
)

foreach ($target in $targets) {
    if (-not (Test-Path -LiteralPath $target)) {
        Write-Host "Not present: $target"
        continue
    }

    $resolved = Resolve-Path -LiteralPath $target -ErrorAction Stop
    $full = $resolved.Path
    $prefix = $hub.Path.TrimEnd("\") + "\"
    if (-not $full.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove path outside hub: $full"
    }
    if ((Split-Path -Leaf $full) -ne $CacheName) {
        throw "Refusing to remove unexpected cache name: $full"
    }

    Write-Host "Removing: $full"
    Remove-Item -LiteralPath $full -Recurse -Force
}
