param(
    [int]$Port = 8888,
    [string]$HostName = "127.0.0.1",
    [switch]$OpenBrowser
)

$ErrorActionPreference = "Stop"

$unsloth = "C:\Users\Sitr3n\.unsloth\studio\bin\unsloth.exe"
$iaRoot = "C:\IA"
$hfHome = Join-Path $iaRoot "hf-home"

if (-not (Test-Path -LiteralPath $unsloth)) {
    throw "Unsloth CLI not found: $unsloth"
}

New-Item -ItemType Directory -Force -Path $hfHome | Out-Null
$env:HF_HOME = $hfHome
$env:HF_HUB_CACHE = Join-Path $hfHome "hub"
$env:HF_XET_CACHE = Join-Path $hfHome "xet"
$env:UNSLOTH_STUDIO_ALLOW_STDIO_MCP = "1"
$env:LOCAL_LLAMA_UNSLOTH_FORCE_PROVIDER = "1"
$env:LOCAL_LLAMA_UNSLOTH_PROVIDER_TYPE = "llama_cpp"
$env:LOCAL_LLAMA_UNSLOTH_PROXY_URL = "http://127.0.0.1:8090/v1"
$env:LOCAL_LLAMA_MODEL = "local-model"

if ($OpenBrowser) {
    Start-Process "http://$HostName`:$Port"
}

& $unsloth studio `
    --host $HostName `
    --port $Port `
    --no-cloudflare `
    --no-secure `
    --enable-tools

exit $LASTEXITCODE
