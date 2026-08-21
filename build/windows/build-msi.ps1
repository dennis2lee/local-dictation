<#
.SYNOPSIS
  Build the Local Dictation MSI.

.DESCRIPTION
  Runs on Windows with the WiX Toolset v5 dotnet tool. The Go executable and the
  server payload have to be staged first — either by running
  build\windows\build-exe.sh under Git Bash / WSL, or by letting this script do
  it with -Build.

  Prerequisites:
    winget install Microsoft.DotNet.SDK.8      (or any .NET 6+ SDK)
    dotnet tool install --global wix --version 5.0.2
    wix extension add -g WixToolset.UI.wixext/5.0.2
    wix extension add -g WixToolset.Util.wixext/5.0.2

  Pin the 5.x version. Package.wxs targets the v4/v5 schema, and WiX v7 refuses
  to run at all until an Open Source Maintenance Fee EULA is accepted.

  A model is not packaged. See docs\model-setup.md.

.EXAMPLE
  .\build-msi.ps1 -Version 0.1.0
  .\build-msi.ps1 -Version 0.1.0 -Build -Sign "My Code Signing Cert"
#>
[CmdletBinding()]
param(
    [string]$Version = '0.1.0',
    [string]$Stage = '',
    [string]$Output = '',
    [string]$Arch = 'x64',
    [switch]$Build,
    [string]$Sign = '',
    [string]$TimestampUrl = 'http://timestamp.digicert.com'
)

$ErrorActionPreference = 'Stop'

$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
if (-not $Stage)  { $Stage  = Join-Path $root 'dist\windows-amd64' }
if (-not $Output) { $Output = Join-Path $root 'dist' }

function Info($message) { Write-Host "==> $message" }

if ($Build) {
    Info 'Building the executable and staging the payload'
    $bash = Get-Command bash -ErrorAction SilentlyContinue
    if (-not $bash) { throw 'bash not found. Install Git for Windows, or run build-exe.sh yourself.' }
    & bash (Join-Path $root 'build/windows/build-exe.sh') --version $Version --output (Join-Path $root 'dist')
    if ($LASTEXITCODE -ne 0) { throw 'build-exe.sh failed' }
}

if (-not (Test-Path (Join-Path $Stage 'local-dictation.exe'))) {
    throw "No staged payload at $Stage. Run build-exe.sh first, or pass -Build."
}

# WiX requires an RTF licence file for the UI. Generate a minimal one from the
# repository's LICENSE so the installer never shows a stale copy.
$licenseSource = Join-Path $root 'LICENSE'
$licenseRtf = Join-Path $Stage 'License.rtf'
if (Test-Path $licenseSource) {
    $text = (Get-Content $licenseSource -Raw) -replace '\\', '\\\\' -replace '([{}])', '\$1'
    $text = $text -replace "`r`n", '\par ' -replace "`n", '\par '
    Set-Content -Path $licenseRtf -Encoding ascii -Value "{\rtf1\ansi\deff0{\fonttbl{\f0 Segoe UI;}}\fs18 $text }"
} else {
    Set-Content -Path $licenseRtf -Encoding ascii -Value '{\rtf1\ansi\deff0{\fonttbl{\f0 Segoe UI;}}\fs18 Local Dictation. Internal use. \par }'
}

$wix = Get-Command wix -ErrorAction SilentlyContinue
if (-not $wix) { throw 'wix not found. Run: dotnet tool install --global wix --version 5.0.2' }

$wixVersion = (& wix --version 2>&1 | Out-String).Trim()
if ($wixVersion -notmatch '^[45]\.') {
    Write-Warning "WiX $wixVersion is installed; Package.wxs targets the v4/v5 schema. If the build fails, install 5.0.2."
}

$msi = Join-Path $Output "LocalDictation-$Version-$Arch.msi"
New-Item -ItemType Directory -Force -Path $Output | Out-Null

Info "Building $msi"
& wix build `
    -arch $Arch `
    -define "Version=$Version" `
    -bindpath "stage=$Stage" `
    -ext WixToolset.UI.wixext `
    -ext WixToolset.Util.wixext `
    -out $msi `
    (Join-Path $PSScriptRoot 'Package.wxs')
if ($LASTEXITCODE -ne 0) { throw 'wix build failed' }

if ($Sign) {
    Info "Signing with $Sign"
    # Sign the executable inside the package as well as the package itself;
    # SmartScreen looks at both.
    & signtool sign /n $Sign /fd SHA256 /tr $TimestampUrl /td SHA256 (Join-Path $Stage 'local-dictation.exe')
    if ($LASTEXITCODE -ne 0) { throw 'signing the executable failed' }
    & signtool sign /n $Sign /fd SHA256 /tr $TimestampUrl /td SHA256 $msi
    if ($LASTEXITCODE -ne 0) { throw 'signing the package failed' }
}

Info 'Built:'
Get-Item $msi | Format-List Name, Length, LastWriteTime
Write-Host "  sha256: $((Get-FileHash $msi -Algorithm SHA256).Hash.ToLower())"
