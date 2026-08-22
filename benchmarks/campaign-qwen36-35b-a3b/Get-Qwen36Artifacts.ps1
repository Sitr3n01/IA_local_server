<#
.SYNOPSIS
Downloads and hash-verifies the Qwen3.6-35B-A3B GGUF candidates for the gfx1201
campaign.

.DESCRIPTION
Download happens outside the provider and is never triggered by an inference
request, per docs/MODEL_PROMOTION.md. Each file is pinned to an immutable
revision and checked against the SHA-256 that Hugging Face's LFS metadata
publishes for that revision, so a truncated or substituted artifact fails here
rather than in a benchmark cell.

Files are fetched to <name>.part and only renamed after the hash matches, which
makes an interrupted run resumable and makes a present file mean a verified file.
#>
[CmdletBinding()]
param(
    [string]$Destination = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$Repository  = 'unsloth/Qwen3.6-35B-A3B-GGUF',
    [string]$Revision    = 'a483e9e6cbd595906af30beda3187c2663a1118c',
    [string[]]$Files     = @(
        'Qwen3.6-35B-A3B-UD-Q3_K_XL.gguf',
        'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf',
        'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf',
        'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'
    ),
    [int]$LimitRateMiB = 0
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Published by the repository's LFS metadata at $Revision, not computed here.
# Recorded so a mismatch is a refusal instead of a note in a report.
$expected = @{
    'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf' = '96b9c0af5c77a4ecaabe3983175112b5ece763261c1ece12b2494b692a70dad7'
    'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'  = '212ccdf37d416167ce8dcd7e3a59bcd45b30ac7531822a1e7bb79bfbacb2d1aa'
    'Qwen3.6-35B-A3B-UD-Q3_K_M.gguf'  = '1b715841683f960bd9a49f008181bd910ee169b78d4cf465b6fde7f4d929ff99'
    'Qwen3.6-35B-A3B-UD-Q3_K_XL.gguf' = 'a832b9689925f1bd335bbe985cdfb06c36bf2cf268f4f8f6eceafa3ceb515617'
    'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf'  = '649d7508507b84638732c4f52c24c8b15843c6dca2f3ff793ae07c14a67ebbb3'
    'Qwen3.6-35B-A3B-UD-IQ4_NL.gguf'  = '0d17e255dc257a11f398ed4bc8d62412d8ce9ca24b3fce2947d962e4bfed5758'
    'Qwen3.6-35B-A3B-MXFP4_MOE.gguf'  = '2fdd20997c4d88ee25f70f500c61f8b999378d92ab055f9d450fc70d617158d3'
}

New-Item -ItemType Directory -Force -Path $Destination | Out-Null
$results = [System.Collections.Generic.List[object]]::new()

foreach ($file in $Files) {
    if (-not $expected.ContainsKey($file)) {
        throw "No pinned SHA-256 for $file; add it before downloading."
    }
    $target  = Join-Path $Destination $file
    $partial = "$target.part"
    $want    = $expected[$file]

    if (Test-Path -LiteralPath $target) {
        Write-Host "[skip] already present: $file"
        $have = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
        $results.Add([pscustomobject]@{ file = $file; bytes = (Get-Item -LiteralPath $target).Length; sha256 = $have; verified = ($have -eq $want); downloaded = $false })
        continue
    }

    $url = "https://huggingface.co/$Repository/resolve/$Revision/$([uri]::EscapeDataString($file))"
    Write-Host "[get ] $file"
    $curlArgs = @('-L', '--fail', '--retry', '8', '--retry-delay', '5', '--retry-all-errors', '-C', '-', '-o', $partial, $url)
    if ($LimitRateMiB -gt 0) { $curlArgs = @('--limit-rate', "${LimitRateMiB}M") + $curlArgs }
    & curl.exe @curlArgs
    if ($LASTEXITCODE -ne 0) { throw "curl failed for $file (exit $LASTEXITCODE)" }

    Write-Host "[hash] $file"
    $have = (Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($have -ne $want) {
        Remove-Item -LiteralPath $partial -Force
        throw "SHA-256 mismatch for ${file}: got $have, expected $want"
    }
    Move-Item -LiteralPath $partial -Destination $target -Force
    Write-Host "[ok  ] $file"
    $results.Add([pscustomobject]@{ file = $file; bytes = (Get-Item -LiteralPath $target).Length; sha256 = $have; verified = $true; downloaded = $true })
}

$results | ConvertTo-Json -Depth 4
