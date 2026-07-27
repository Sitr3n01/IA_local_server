[CmdletBinding()]
param(
    [string]$DataUrl = 'http://127.0.0.1:18090',
    [string]$CredentialHelper = 'C:\IA\local-ai-v2\bin\cia-credential.exe',
    [string]$Node = 'C:\Users\Sitr3n\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin\node.exe'
)

$ErrorActionPreference = 'Stop'
foreach ($required in @($CredentialHelper, $Node)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required executable not found: $required"
    }
}

$temporaryBody = [IO.Path]::GetTempFileName()
$token = $null
$client = $null
$request = $null
$response = $null
try {
    $javascript = @'
const fs = require('fs');
const zlib = require('zlib');
const body = Buffer.from(JSON.stringify({ model: 'unknown-zstd', input: 'synthetic' }));
fs.writeFileSync(process.argv[1], zlib.zstdCompressSync(body));
'@
    & $Node -e $javascript $temporaryBody
    if ($LASTEXITCODE -ne 0) {
        throw 'Node zstd compression failed.'
    }

    Add-Type -AssemblyName System.Net.Http
    $token = (& $CredentialHelper get inference 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($token)) {
        throw 'Unable to obtain the inference credential.'
    }

    $client = [Net.Http.HttpClient]::new()
    $request = [Net.Http.HttpRequestMessage]::new(
        [Net.Http.HttpMethod]::Post,
        "$($DataUrl.TrimEnd('/'))/v1/responses"
    )
    $request.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $token)
    $content = [Net.Http.ByteArrayContent]::new([IO.File]::ReadAllBytes($temporaryBody))
    $content.Headers.ContentType = [Net.Http.Headers.MediaTypeHeaderValue]::new('application/json')
    $content.Headers.ContentEncoding.Add('zstd')
    $request.Content = $content

    $response = $client.SendAsync($request).GetAwaiter().GetResult()
    if ([int]$response.StatusCode -ne 404) {
        throw "Expected model_not_found (404) after zstd decoding; received $([int]$response.StatusCode)."
    }

    [pscustomobject]@{
        pass = $true
        encoding = 'zstd'
        status = [int]$response.StatusCode
        wire_bytes = (Get-Item -LiteralPath $temporaryBody).Length
        expected_result = 'model_not_found'
    } | ConvertTo-Json -Compress
}
finally {
    if ($response) { $response.Dispose() }
    if ($request) { $request.Dispose() }
    if ($client) { $client.Dispose() }
    $token = $null
    if (Test-Path -LiteralPath $temporaryBody) {
        Remove-Item -LiteralPath $temporaryBody -Force
    }
}
