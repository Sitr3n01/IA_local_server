<#
.SYNOPSIS
Downloads and hash-verifies the Ornith-1.5-35B-A3B GGUF candidates for the
gfx1201 campaign.

.DESCRIPTION
Download happens outside the provider and is never triggered by an inference
request, per docs/MODEL_PROMOTION.md. Each file is pinned to an immutable
revision and checked against the SHA-256 that Hugging Face's LFS metadata
publishes for that revision, so a truncated or substituted artifact fails here
rather than in a benchmark cell.

Files are fetched to <name>.part and only renamed after the hash matches, which
makes an interrupted run resumable and makes a present file mean a verified file.

Provenance note. Unsloth publishes no GGUF repository for this model, so the
artifacts come from bartowski, whose ladder is imatrix-calibrated but is not the
UD recipe the rest of this roster uses. The campaign therefore matches file SIZE
against the Qwen3.6 artifacts already on disk rather than matching quant names,
because the decision this campaign informs is quality per VRAM at a fixed
budget, and size is the budget:

    Qwen3.6 UD-Q2_K_XL  12,290,628,576  <->  Ornith IQ2_M    12,543,403,680  (+2.1%)
    Qwen3.6 UD-Q3_K_S   15,359,196,128  <->  Ornith IQ3_XXS  15,340,447,392  (-0.1%)
    Qwen3.6 UD-IQ4_XS   17,730,509,792  <->  Ornith Q3_K_XL  17,801,889,440  (+0.4%)

.EXAMPLE
./Get-Ornith15Artifacts.ps1 -LimitRateMiB 8
#>
[CmdletBinding()]
param(
    [string]$Destination = 'C:\IA\models\Ornith-1.5-35B-A3B-GGUF',
    [string]$Repository  = 'bartowski/Ornith-1.5-35B-A3B-GGUF',
    [string]$Revision    = '64b0493d34a5ca4c1b4ad67bb99b41d74b4f07d6',
    [string[]]$Files     = @(
        'Ornith-1.5-35B-A3B-IQ2_M.gguf',
        'Ornith-1.5-35B-A3B-IQ3_XXS.gguf',
        'Ornith-1.5-35B-A3B-Q3_K_XL.gguf'
    ),
    [int]$LimitRateMiB = 0,
    # Refuse to start a download that would leave the volume below this. The
    # campaign itself needs headroom: peak commit measured on Qwen3.6 was ~34
    # GiB against ~31 GiB of RAM, which is pagefile.
    [int]$MinFreeGiBAfter = 20
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Published by the repository's LFS metadata at $Revision, not computed here.
# Recorded so a mismatch is a refusal instead of a note in a report.
$expected = @{
    'Ornith-1.5-35B-A3B-IQ2_XXS.gguf' = '14963c6830ae8287ad42a53c0fd87ca9fc67a8de8da1d18e3a076a348e96a32f'
    'Ornith-1.5-35B-A3B-IQ2_XS.gguf'  = '2bc807c139798cd298ea9371d1e24ef851c27ec8a1964e761d55ae015de13737'
    'Ornith-1.5-35B-A3B-IQ2_S.gguf'   = '49b923ebe5f4d4a4384ccb86dd2b93effacc96ec1a3054a0192cc61dc045ebab'
    'Ornith-1.5-35B-A3B-IQ2_M.gguf'   = 'be92ed1eb2da35876e91a5551c527ddcce450d49ec53aba92f03e26adba7b996'
    'Ornith-1.5-35B-A3B-Q2_K.gguf'    = '6e5742a1a8c263a9c77cd7ffd5338a290a29c1d5d3dd4d6deee76351b0718fe1'
    'Ornith-1.5-35B-A3B-Q2_K_L.gguf'  = '5680ed38b8dae06fec018e22d2b5bd7f2fdcd8e48de6c73c48c9e62d3447f0dc'
    'Ornith-1.5-35B-A3B-IQ3_XXS.gguf' = '8918ccb9ee29abe3875efec0c3e86f0e35ef0b8a03e3f0e1c422869859a518d0'
    'Ornith-1.5-35B-A3B-Q3_K_S.gguf'  = 'f2e3c4b472a353c3d22d2eece67cec20ce1f9b6c646efd433c4892c1e4d63ffb'
    'Ornith-1.5-35B-A3B-IQ3_XS.gguf'  = '202b8b39bcc35a0039d8e65af0697bf1ce969a8a99653356cc7911d2439f8828'
    'Ornith-1.5-35B-A3B-Q3_K_M.gguf'  = '5aa203a8c0b9998aa41a625ef1a6241c7ffb26b99acce9965a4986d3c3dc2943'
    'Ornith-1.5-35B-A3B-IQ3_M.gguf'   = '3d220d65381dbec446a6c745d2bc8a80fe2bed9f532abf7163c5764e62abefbf'
    'Ornith-1.5-35B-A3B-Q3_K_L.gguf'  = '9c3c71ae3a190cc55dcb1b99d3e931f0220803e98fb790ec4bc16313de6f2142'
    'Ornith-1.5-35B-A3B-Q3_K_XL.gguf' = '782359e862536c3d743899e83906312ae66b9512ba8a27ad6013bb1d3032cccb'
    'Ornith-1.5-35B-A3B-IQ4_XS.gguf'  = 'd6aef57fa948e9bba3ca4959b3c237ed898c605471f48c73a32cedbd24aabe70'
    'Ornith-1.5-35B-A3B-IQ4_NL.gguf'  = '7d198c77fc6fd81b2cb5dc517df70b164f245e1046ed4e5ff2d3e0fb483e9eb6'
    'Ornith-1.5-35B-A3B-Q4_K_S.gguf'  = '7666f715af2cab0e880ebf4494ee36c13db1e68392d7c513806502c14ff549d3'
    'Ornith-1.5-35B-A3B-Q4_K_M.gguf'  = '12d8d5c01bae7f23ea4822b2f96ba069d531f827d02a57c31002f2f95e72614a'
    'Ornith-1.5-35B-A3B-imatrix.gguf' = '8d5b16938373b678b68c3081ef3f6985b789df9b25c18af130909658a47c0228'
}

# Byte size at $Revision, same source as the hashes. Used for the disk guard and
# so a wrong-file download fails before it is hashed.
$expectedBytes = @{
    'Ornith-1.5-35B-A3B-IQ2_XXS.gguf' = 10255140512
    'Ornith-1.5-35B-A3B-IQ2_XS.gguf'  = 11273832096
    'Ornith-1.5-35B-A3B-IQ2_S.gguf'   = 11486963360
    'Ornith-1.5-35B-A3B-IQ2_M.gguf'   = 12543403680
    'Ornith-1.5-35B-A3B-Q2_K.gguf'    = 13085779616
    'Ornith-1.5-35B-A3B-Q2_K_L.gguf'  = 13582419616
    'Ornith-1.5-35B-A3B-IQ3_XXS.gguf' = 15340447392
    'Ornith-1.5-35B-A3B-Q3_K_S.gguf'  = 15983920800
    'Ornith-1.5-35B-A3B-IQ3_XS.gguf'  = 16689088160
    'Ornith-1.5-35B-A3B-Q3_K_M.gguf'  = 16696952480
    'Ornith-1.5-35B-A3B-Q3_K_L.gguf'  = 17356900000
    'Ornith-1.5-35B-A3B-IQ3_M.gguf'   = 17370662560
    'Ornith-1.5-35B-A3B-Q3_K_XL.gguf' = 17801889440
    'Ornith-1.5-35B-A3B-IQ4_XS.gguf'  = 19278554784
    'Ornith-1.5-35B-A3B-IQ4_NL.gguf'  = 20333979296
    'Ornith-1.5-35B-A3B-Q4_K_S.gguf'  = 21065361056
    'Ornith-1.5-35B-A3B-Q4_K_M.gguf'  = 21864081056
    'Ornith-1.5-35B-A3B-imatrix.gguf' = 192223936
}

New-Item -ItemType Directory -Force -Path $Destination | Out-Null

# ---- disk guard -----------------------------------------------------------
$drive = (Get-Item -LiteralPath $Destination).PSDrive.Name
$free  = (Get-PSDrive -Name $drive).Free
$need  = 0
foreach ($file in $Files) {
    if (Test-Path -LiteralPath (Join-Path $Destination $file)) { continue }
    if (-not $expectedBytes.ContainsKey($file)) { throw "No pinned size for $file; add it before downloading." }
    $need += $expectedBytes[$file]
}
$freeGiB  = [math]::Round($free / 1GB, 1)
$needGiB  = [math]::Round($need / 1GB, 1)
$afterGiB = [math]::Round(($free - $need) / 1GB, 1)
Write-Host "[disk] free $freeGiB GiB, need $needGiB GiB, would leave $afterGiB GiB"
if ($afterGiB -lt $MinFreeGiBAfter) {
    throw "Refusing to download: would leave $afterGiB GiB on ${drive}:, below -MinFreeGiBAfter $MinFreeGiBAfter."
}

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

    $encoded = [uri]::EscapeDataString($file)
    $url = "https://huggingface.co/$Repository/resolve/$Revision/$encoded"
    Write-Host "[get ] $file"
    $curlArgs = @('-L', '--fail', '--retry', '8', '--retry-delay', '5', '--retry-all-errors', '-C', '-', '-o', $partial, $url)
    if ($LimitRateMiB -gt 0) { $curlArgs = @('--limit-rate', "$LimitRateMiB" + 'M') + $curlArgs }
    & curl.exe @curlArgs
    if ($LASTEXITCODE -ne 0) { throw "curl failed for $file (exit $LASTEXITCODE)" }

    $gotBytes = (Get-Item -LiteralPath $partial).Length
    $wantBytes = $expectedBytes[$file]
    if ($gotBytes -ne $wantBytes) {
        Remove-Item -LiteralPath $partial -Force
        throw "Size mismatch for ${file}: got $gotBytes, expected $wantBytes."
    }

    Write-Host "[hash] $file"
    $have = (Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($have -ne $want) {
        Remove-Item -LiteralPath $partial -Force
        throw "SHA-256 mismatch for ${file}: got $have, expected $want"
    }
    Move-Item -LiteralPath $partial -Destination $target -Force
    Write-Host "[ok  ] $file"
    $results.Add([pscustomobject]@{ file = $file; bytes = $gotBytes; sha256 = $have; verified = $true; downloaded = $true })
}

$results | ConvertTo-Json -Depth 4
