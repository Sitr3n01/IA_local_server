[CmdletBinding()]
param(
	[ValidatePattern('^[a-z0-9][a-z0-9._-]{0,127}$')]
	[string]$Model = 'local-coding',
	[ValidateRange(1024, 65535)]
	[int]$Port = 8888,
	[ValidateRange(5, 300)]
	[int]$StartupTimeoutSeconds = 60
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$manifestPath = 'C:\IA\local-ai-v2\config\models.yaml'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
	throw "Installed v2 manifest not found: $manifestPath"
}
$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$allowedModels = @($manifest.models | Where-Object {
		$_.id -eq $Model -and
		$_.deployments -contains 'final' -and
		$_.state -in @('qualified', 'enabled')
})
if ($allowedModels.Count -ne 1) {
	throw "Model '$Model' is not qualified for the final deployment."
}

$studioRoot = [IO.Path]::GetFullPath((Join-Path $env:USERPROFILE '.unsloth\studio')).TrimEnd('\')
$studioRootPrefix = $studioRoot + '\'
$unslothCandidates = @(
	(Join-Path $studioRoot 'bin\unsloth.exe'),
	(Join-Path $studioRoot 'unsloth_studio\Scripts\unsloth.exe')
)
$trustedUnslothExecutables = @($unslothCandidates |
	Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
	ForEach-Object { (Resolve-Path -LiteralPath $_ -ErrorAction Stop).Path } |
	Select-Object -Unique)
if ($trustedUnslothExecutables.Count -eq 0) {
	throw 'Unsloth Studio was not found.'
}
$unslothPath = $trustedUnslothExecutables[0]
foreach ($trustedExecutablePath in $trustedUnslothExecutables) {
	if (-not $trustedExecutablePath.StartsWith($studioRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
		throw "Unsloth executable is outside the expected Studio root: $trustedExecutablePath"
	}
}

function Test-UnslothListenerOwner {
	param(
		[Parameter(Mandatory = $true)]
		[object]$Listener,
		[Parameter(Mandatory = $true)]
		[string[]]$ExpectedExecutables,
		[Parameter(Mandatory = $true)]
		[string]$ExpectedStudioRoot
	)

	$canonicalExpectedExecutables = @($ExpectedExecutables | ForEach-Object { [IO.Path]::GetFullPath($_) })
	$currentProcessId = [uint32]$Listener.OwningProcess
	$rootPrefix = $ExpectedStudioRoot.TrimEnd('\') + '\'
	if ($canonicalExpectedExecutables.Count -eq 0 -or @($canonicalExpectedExecutables | Where-Object {
		-not $_.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)
	}).Count -gt 0) {
		return $false
	}
	$visited = @{}
	for ($depth = 0; $depth -lt 12 -and $currentProcessId -ne 0; $depth++) {
		$key = [string]$currentProcessId
		if ($visited.ContainsKey($key)) { break }
		$visited[$key] = $true

		$process = Get-CimInstance Win32_Process -Filter "ProcessId = $currentProcessId" -ErrorAction SilentlyContinue
		if (-not $process) { break }
		$commandLine = [string]$process.CommandLine
		$hasStudioCommand = $commandLine -match '(?i)(^|\s)studio(\s|$)'
		$processPath = [string]$process.ExecutablePath
		$trustedExecutable = $false
		if (-not [string]::IsNullOrWhiteSpace($processPath)) {
			try {
				$canonicalProcessPath = [IO.Path]::GetFullPath($processPath)
				$trustedExecutable = @($canonicalExpectedExecutables | Where-Object {
					[string]::Equals($canonicalProcessPath, $_, [StringComparison]::OrdinalIgnoreCase)
				}).Count -gt 0
			}
			catch {
				$trustedExecutable = $false
			}
		}
		$trustedCommand = @($canonicalExpectedExecutables | Where-Object {
			$commandLine.IndexOf($_, [StringComparison]::OrdinalIgnoreCase) -ge 0
		}).Count -gt 0
		if ($hasStudioCommand -and ($trustedExecutable -or $trustedCommand)) {
			return $true
		}
		$currentProcessId = [uint32]$process.ParentProcessId
	}
	return $false
}

function Wait-UnslothStudioReady {
	param(
		[Parameter(Mandatory = $true)]
		[string]$URL,
		[Parameter(Mandatory = $true)]
		[int]$ListenPort,
		[Parameter(Mandatory = $true)]
		[string[]]$ExpectedExecutables,
		[Parameter(Mandatory = $true)]
		[string]$ExpectedStudioRoot,
		[Parameter(Mandatory = $true)]
		[int]$TimeoutSeconds
	)

	$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
	do {
		$listeners = @(Get-NetTCPConnection -State Listen -LocalPort $ListenPort -ErrorAction SilentlyContinue)
		if ($listeners.Count -gt 0) {
			$invalidListeners = @($listeners | Where-Object {
				$_.LocalAddress -ne '127.0.0.1' -or
				-not (Test-UnslothListenerOwner -Listener $_ -ExpectedExecutables $ExpectedExecutables -ExpectedStudioRoot $ExpectedStudioRoot)
			})
			if ($invalidListeners.Count -gt 0) {
				throw "Port $ListenPort is owned by a process other than the expected loopback Unsloth Studio."
			}
			try {
				$response = Invoke-WebRequest -UseBasicParsing -Uri $URL -TimeoutSec 2
				if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 500) {
					return
				}
			}
			catch {
				# The process may have bound before its HTTP application is ready.
			}
		}
		Start-Sleep -Milliseconds 500
	} while ([DateTime]::UtcNow -lt $deadline)

	throw "Unsloth Studio did not become ready on literal loopback port $ListenPort within $TimeoutSeconds seconds."
}

$hfHome = 'C:\IA\hf-home'
New-Item -ItemType Directory -Force -Path $hfHome | Out-Null
$env:HF_HOME = $hfHome
$env:HF_HUB_CACHE = Join-Path $hfHome 'hub'
$env:HF_XET_CACHE = Join-Path $hfHome 'xet'

$studioURL = "http://127.0.0.1:$Port"
$existingListeners = @(Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue)
if ($existingListeners.Count -eq 0) {
	$studioArguments = @(
		'studio',
		'--host', '127.0.0.1',
		'--port', [string]$Port,
		'--no-cloudflare',
		'--no-secure',
		'--enable-tools'
	)
	Start-Process -FilePath $unslothPath -ArgumentList $studioArguments -WorkingDirectory (Split-Path -Parent $unslothPath) -WindowStyle Hidden | Out-Null
}

# The supported Custom provider is configured and selected in Unsloth Studio.
# This launcher leaves private application state and retired connections alone.
Wait-UnslothStudioReady `
	-URL $studioURL `
	-ListenPort $Port `
	-ExpectedExecutables $trustedUnslothExecutables `
	-ExpectedStudioRoot $studioRoot `
	-TimeoutSeconds $StartupTimeoutSeconds
Start-Process -FilePath $studioURL
exit 0
