<#
.SYNOPSIS
    Builds the Linux installation bundle on Windows, without Docker, WSL or a Linux host.

.DESCRIPTION
    Cross-compiles the panel for linux/amd64, takes the managed endpoint from -Endpoint,
    artifacts\trusttunnel_endpoint or the newest GitHub release, and packs
    artifacts\trusttunnel-dev.tar.gz with the same entries, order, permissions and LF
    endings as scripts/build-dev-bundle.sh. The .sha256 is written next to it.

    Use this when the managed endpoint is already built: the endpoint is a Rust binary
    pinned to upstream and changes rarely, so only the panel has to be rebuilt for a
    panel change. To rebuild the endpoint itself, use Docker: scripts\build-local.ps1 -Bundle.

.PARAMETER Endpoint
    Path to the managed Linux endpoint. Defaults to artifacts\trusttunnel_endpoint, and
    falls back to the copy inside the newest release asset when that file is missing.

.PARAMETER Version
    Version recorded in the panel build. Defaults to dev-<short sha>.



.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\build-dev-bundle.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\build-dev-bundle.ps1 -Version 0.5.9
#>
param(
    [string]$Endpoint,
    [string]$Version
)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$artifacts = Join-Path $root 'artifacts'
$repo = 'divezh/netlane-server'
New-Item -ItemType Directory -Force -Path $artifacts | Out-Null

function Find-Gh {
    $command = Get-Command gh -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    foreach ($candidate in @("$env:ProgramFiles\GitHub CLI\gh.exe", "${env:ProgramFiles(x86)}\GitHub CLI\gh.exe")) {
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    return $null
}

function Get-Endpoint {
    if ($Endpoint) {
        if (-not (Test-Path -LiteralPath $Endpoint)) { throw "Endpoint not found: $Endpoint" }
        return [IO.Path]::GetFullPath($Endpoint)
    }
    $local = Join-Path $artifacts 'trusttunnel_endpoint'
    if (Test-Path -LiteralPath $local) { return $local }
    $gh = Find-Gh
    if (-not $gh) {
        throw "No endpoint: put the managed Linux endpoint in $local, pass -Endpoint, or install the GitHub CLI so it can be taken from the newest release."
    }
    Write-Host 'Endpoint is missing: taking bin/trusttunnel_endpoint from the newest release.'
    $download = Join-Path $artifacts 'endpoint-source.tar.gz'
    & $gh release download --repo $repo --pattern 'trusttunnel-dev.tar.gz' --dir $artifacts --clobber
    if ($LASTEXITCODE -ne 0) { throw 'Could not download the newest release. Save a token first: gh auth login' }
    $asset = Join-Path $artifacts 'trusttunnel-dev.tar.gz'
    Move-Item -LiteralPath $asset -Destination $download -Force
    & tar -xzf $download -C $artifacts ./bin/trusttunnel_endpoint
    if ($LASTEXITCODE -ne 0) { throw "Could not unpack $download" }
    Move-Item -LiteralPath (Join-Path $artifacts 'bin\trusttunnel_endpoint') -Destination $local -Force
    Remove-Item -LiteralPath (Join-Path $artifacts 'bin') -Recurse -Force
    Remove-Item -LiteralPath $download -Force
    return $local
}

$endpoint = Get-Endpoint
$bytes = [IO.File]::ReadAllBytes($endpoint)
if (-not ($bytes[0] -eq 0x7F -and $bytes[1] -eq 0x45 -and $bytes[2] -eq 0x4C -and $bytes[3] -eq 0x46 -and $bytes[18] -eq 0x3E)) {
    throw "$endpoint is not a linux/amd64 ELF binary. The bundle needs the managed endpoint, not a Windows build."
}
$text = [Text.Encoding]::ASCII.GetString($bytes)
if ($text -notmatch 'admin-preview\.5') {
    Write-Warning 'The endpoint does not report the -admin-preview.5 build; the panel may refuse it.'
}

$revision = (& git -C $root rev-parse --short HEAD 2>&1) -join ''
if (-not $Version) { $Version = "dev-$revision" }
Write-Host "Revision $revision, version $Version"
Write-Host "Endpoint $endpoint ($([math]::Round($bytes.Length/1MB,2)) MB)"

Push-Location $root
try {
    Write-Host ''
    Write-Host '== Panel (linux/amd64)'
    $previous = @{ CGO = $env:CGO_ENABLED; OS = $env:GOOS; Arch = $env:GOARCH }
    $env:CGO_ENABLED = '0'; $env:GOOS = 'linux'; $env:GOARCH = 'amd64'
    try {
        & go build -trimpath -ldflags="-s -w -X main.version=$Version" -o (Join-Path $artifacts 'trusttunnel-admin') ./cmd/trusttunnel-admin
        if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    } finally {
        $env:CGO_ENABLED = $previous.CGO; $env:GOOS = $previous.OS; $env:GOARCH = $previous.Arch
    }

    $panel = [IO.File]::ReadAllBytes((Join-Path $artifacts 'trusttunnel-admin'))
    if (-not ($panel[0] -eq 0x7F -and $panel[18] -eq 0x3E)) { throw 'The panel is not a linux/amd64 ELF binary.' }
    Write-Host "   artifacts\trusttunnel-admin  $([math]::Round($panel.Length/1MB,2)) MB"

    Write-Host ''
    Write-Host '== Bundle'
    $arguments = @((Join-Path $root 'scripts\pack-dev-bundle.py'), '--repo', $root, '--artifacts', $artifacts)
    & python @arguments
    if ($LASTEXITCODE -ne 0) { throw 'pack-dev-bundle.py failed' }
} finally { Pop-Location }

Write-Host ''
Write-Host 'Next: load artifacts\trusttunnel-dev.tar.gz and its .sha256 to /root on the VPS, then'
Write-Host '  cd /root && sha256sum -c trusttunnel-dev.tar.gz.sha256 && netlane-update --file /root/trusttunnel-dev.tar.gz --yes'
