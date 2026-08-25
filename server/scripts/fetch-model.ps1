<#
.SYNOPSIS
  Download a Whisper model for Local Dictation on Windows.

.DESCRIPTION
  The PowerShell twin of fetch-model.sh. Models are not shipped inside the
  installer: they are large, they carry their own licence, and sites mirror them
  differently. This script is the supported way to put one on disk.

.EXAMPLE
  .\fetch-model.ps1                              # large-v3 into the default dir
  .\fetch-model.ps1 -Model large-v3-turbo        # turbo (half the size, much faster)
  .\fetch-model.ps1 -Model base                  # draft model, for live partials
  .\fetch-model.ps1 -Model large-v3-turbo-openvino-int8   # for an Intel GPU
  .\fetch-model.ps1 -Model all -Dest D:\models   # every model + the VAD
  .\fetch-model.ps1 -List                        # show sizes, download nothing
  .\fetch-model.ps1 -Verify -Dest D:\models      # re-check an existing install
#>
[CmdletBinding()]
param(
    [ValidateSet('large-v3', 'large-v3-turbo', 'base', 'vad', 'all',
                 'large-v3-turbo-openvino-int8',
                 'large-v3-turbo-openvino-fp16',
                 'large-v3-turbo-openvino-int4')]
    [string]$Model = 'large-v3',

    [string]$Dest = "$env:LOCALAPPDATA\LocalDictation\models",
    [string]$Repo = '',
    [switch]$MetadataOnly,
    [switch]$Force,
    [switch]$List,
    [switch]$Verify
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'Continue'

# CTranslate2 conversions — the format faster-whisper loads directly. The plain
# openai/* repositories are PyTorch checkpoints and will not work here.
$Repos = @{
    'large-v3'       = 'Systran/faster-whisper-large-v3'
    'large-v3-turbo' = 'deepdml/faster-whisper-large-v3-turbo-ct2'
    # The draft model: it writes only the live partial text and nothing that is
    # kept, which on CPU is the single biggest latency change available.
    'base'           = 'Systran/faster-whisper-base'
    # OpenVINO IR exports, for --backend openvino on an Intel GPU. A different
    # format again: these hold openvino_encoder_model.xml rather than model.bin,
    # and the two are not interchangeable — the server refuses the wrong one at
    # startup rather than failing inside the first decode.
    #
    # Three precisions because which to ship is a measurement. INT8 is the
    # expected answer; openvino-benchmark.py is what settles it.
    'large-v3-turbo-openvino-int8' = 'OpenVINO/whisper-large-v3-turbo-int8-ov'
    'large-v3-turbo-openvino-fp16' = 'OpenVINO/whisper-large-v3-turbo-fp16-ov'
    'large-v3-turbo-openvino-int4' = 'OpenVINO/whisper-large-v3-turbo-int4-ov'
}
# Only the fallback for when the API is unreachable. The file set differs
# between conversions — the large-v3 repos carry vocabulary.json and a
# preprocessor config, base carries vocabulary.txt and no preprocessor — so the
# real list is asked for, not assumed.
$FallbackFiles = @('config.json', 'model.bin', 'preprocessor_config.json', 'tokenizer.json', 'vocabulary.json')
$SileroUrl = 'https://raw.githubusercontent.com/snakers4/silero-vad/master/src/silero_vad/data/silero_vad.onnx'
$Endpoint = if ($env:HF_ENDPOINT) { $env:HF_ENDPOINT } else { 'https://huggingface.co' }

function Format-Size([long]$Bytes) {
    if ($Bytes -le 0) { return 'unknown' }
    $units = 'B', 'KiB', 'MiB', 'GiB', 'TiB'
    $index = 0
    $value = [double]$Bytes
    while ($value -ge 1024 -and $index -lt 4) { $value /= 1024; $index++ }
    if ($index -eq 0) { '{0} {1}' -f [int]$value, $units[$index] }
    else { '{0:N1} {1}' -f $value, $units[$index] }
}

function Get-RemoteSize([string]$Url) {
    try { [long](Invoke-WebRequest -Uri $Url -Method Head -MaximumRedirection 5).Headers['Content-Length'][0] }
    catch { 0 }
}

function Get-Sha256([string]$Path) { (Get-FileHash -Path $Path -Algorithm SHA256).Hash.ToLower() }

function Save-File([string]$Url, [string]$Target) {
    $name = Split-Path $Target -Leaf
    if ((Test-Path $Target) -and -not $Force) { Write-Host "  = $name (already present)"; return }
    New-Item -ItemType Directory -Force -Path (Split-Path $Target -Parent) | Out-Null
    $partial = "$Target.part"
    Invoke-WebRequest -Uri $Url -OutFile $partial -MaximumRedirection 5
    Move-Item -Force $partial $Target
    Write-Host "  + $name ($(Format-Size (Get-Item $Target).Length))"
}

function Write-Sums([string]$Directory) {
    $lines = Get-ChildItem -File $Directory |
        Where-Object { $_.Name -ne 'SHA256SUMS' } |
        ForEach-Object { '{0}  {1}' -f (Get-Sha256 $_.FullName), $_.Name }
    Set-Content -Path (Join-Path $Directory 'SHA256SUMS') -Value $lines -Encoding ascii
    Write-Host "  wrote $Directory\SHA256SUMS"
}

# Ask the repository what it actually contains, skipping repository furniture.
function Get-RepoFiles([string]$Source) {
    $skip = @('.gitattributes', 'README.md', '.gitignore', 'SHA256SUMS')
    try {
        $listing = Invoke-RestMethod -Uri "$Endpoint/api/models/$Source" -TimeoutSec 20
        $names = @($listing.siblings | ForEach-Object { $_.rfilename } |
                   Where-Object { $_ -and $skip -notcontains $_ -and $_ -notlike '*/*' })
        if ($names.Count -gt 0) { return $names }
    }
    catch { }
    $FallbackFiles
}

function Get-ModelRepo([string]$Name) {
    if ($Repo) { return $Repo }
    if (-not $Repos.ContainsKey($Name)) { throw "unknown model '$Name'" }
    $Repos[$Name]
}

function Save-Model([string]$Name) {
    $source = Get-ModelRepo $Name
    $target = Join-Path $Dest $Name
    Write-Host "$Name  <-  $source"
    foreach ($file in (Get-RepoFiles $source)) {
        if ($file -eq 'model.bin' -and $MetadataOnly) { Write-Host '  ~ model.bin (skipped: -MetadataOnly)'; continue }
        Save-File "$Endpoint/$source/resolve/main/$file" (Join-Path $target $file)
    }
    Write-Sums $target
}

function Save-Vad() {
    Write-Host 'silero VAD  <-  github.com/snakers4/silero-vad'
    Save-File $SileroUrl (Join-Path $Dest 'silero_vad.onnx')
}

function Show-Sizes() {
    $total = 0L
    foreach ($name in $Repos.Keys | Sort-Object) {
        $source = Get-ModelRepo $name
        Write-Host ('{0,-16} {1}' -f $name, $source)
        foreach ($file in (Get-RepoFiles $source)) {
            $size = Get-RemoteSize "$Endpoint/$source/resolve/main/$file"
            Write-Host ('  {0,-28} {1,10}' -f $file, (Format-Size $size))
            $total += $size
        }
    }
    $vad = Get-RemoteSize $SileroUrl
    Write-Host ('{0,-16} {1}' -f 'vad', 'silero-vad')
    Write-Host ('  {0,-28} {1,10}' -f 'silero_vad.onnx', (Format-Size $vad))
    Write-Host ''
    Write-Host ('{0,-16} {1,10}  (both models + VAD)' -f 'total', (Format-Size ($total + $vad)))
}

function Test-Install() {
    $checked = 0; $failures = 0
    foreach ($sums in Get-ChildItem -Path $Dest -Filter 'SHA256SUMS' -Recurse -ErrorAction SilentlyContinue) {
        $directory = $sums.Directory
        Write-Host "verifying $($directory.Name)"
        foreach ($line in Get-Content $sums.FullName) {
            if (-not $line.Trim()) { continue }
            $expected, $name = $line -split '\s+', 2
            $path = Join-Path $directory.FullName $name.Trim()
            $checked++
            if (-not (Test-Path $path)) { Write-Host "  MISSING $name"; $failures++; continue }
            if ((Get-Sha256 $path) -eq $expected) { Write-Host "  ok      $name" }
            else { Write-Host "  CORRUPT $name"; $failures++ }
        }
    }
    $vadPath = Join-Path $Dest 'silero_vad.onnx'
    if (Test-Path $vadPath) { $checked++; Write-Host "  ok      silero_vad.onnx ($(Format-Size (Get-Item $vadPath).Length))" }
    if ($checked -eq 0) { throw "nothing to verify under $Dest" }
    if ($failures -gt 0) { throw "$failures file(s) failed verification" }
    Write-Host "all $checked file(s) verified"
}

if ($List) { Show-Sizes; return }
if ($Verify) { Test-Install; return }

New-Item -ItemType Directory -Force -Path $Dest | Out-Null
switch ($Model) {
    'vad' { Save-Vad }
    'all' { Save-Model 'large-v3'; Save-Model 'large-v3-turbo'; Save-Model 'base'; Save-Vad }
    default { Save-Model $Model; Save-Vad }
}

Write-Host ''
Write-Host "installed under $Dest"
if ($Model -eq 'base') {
    # base is a draft model. Naming it as model.path would quietly downgrade
    # every transcript the server commits.
    Write-Host ''
    Write-Host 'base is a draft model - it belongs on draft_path, not model.path:'
    Write-Host "  model.draft_path:               $Dest\base"
    Write-Host ''
    Write-Host 'Leave model.path pointing at the accurate model. The draft one only'
    Write-Host 'writes the partial text you watch appear; what you keep is decoded'
    Write-Host 'once, at the end, by the model above it.'
}
elseif ($Model -like 'large-v3-turbo-openvino-*') {
    Write-Host ''
    Write-Host 'an OpenVINO export, for an Intel GPU. In the client:'
    Write-Host '  Settings > Local > Decode on > Intel GPU'
    Write-Host "  Settings > Local > Model directory > $Dest\$Model"
    Write-Host ''
    Write-Host 'Point Model directory at this one, not at large-v3-turbo - that is the'
    Write-Host 'CPU conversion and this backend cannot read it.'
    Write-Host ''
    Write-Host 'Running a server by hand instead:'
    Write-Host "  model.path:                     $Dest\$Model"
    Write-Host '  model.device:                   GPU'
    Write-Host "  streaming.silero_model_path:    $Dest\silero_vad.onnx"
    Write-Host '  and start it with --backend openvino'
}
elseif ($Model -ne 'vad') {
    $installed = if ($Model -eq 'all') { 'large-v3' } else { $Model }
    Write-Host ''
    Write-Host 'put these two lines in your server config:'
    Write-Host "  model.path:                     $Dest\$installed"
    Write-Host "  streaming.silero_model_path:    $Dest\silero_vad.onnx"
}
