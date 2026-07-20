param(
    [switch]$RemoveDocumentsLauncher
)

$ErrorActionPreference = "Stop"

$startupDir = [Environment]::GetFolderPath("Startup")
$documentsDir = [Environment]::GetFolderPath("MyDocuments")

$paths = @(
    (Join-Path $startupDir "Local Llama Model Tray.lnk"),
    (Join-Path $startupDir "Unsloth Studio Local.lnk")
)

if ($RemoveDocumentsLauncher) {
    $paths += Join-Path $documentsDir "Local Llama Model Tray.lnk"
}

$removed = @()
foreach ($path in $paths) {
    if (Test-Path -LiteralPath $path) {
        Remove-Item -LiteralPath $path -Force
        $removed += $path
    }
}

[PSCustomObject]@{
    removed = $removed
    startup_dir = $startupDir
    documents_dir = $documentsDir
}
