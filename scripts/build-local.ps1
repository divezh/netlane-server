<#
.SYNOPSIS
Local Netlane builds on a Windows PC, the same steps as CI.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File scripts\build-local.ps1
  Shows which build tools are installed and what each target needs.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File scripts\build-local.ps1 -Bundle
  Builds the full Linux install bundle (managed endpoint + panel) in Docker.
  Then: scripts\deploy-vps.ps1 -Target root@IP -Domain vpn.example.com -Bundle artifacts\trusttunnel-dev.tar.gz

.DESCRIPTION
-Panel         Linux panel binary artifacts\trusttunnel-admin (Go tests and vet run in Docker when available).
-Bundle        Managed endpoint (Rust, in Docker) and artifacts\trusttunnel-dev.tar.gz for deploy-vps.
-All           Panel and Bundle.
-SkipTests     Build only; skip cargo fmt/clippy/tests and Go tests. Use for quick iterations, never for a release.

When the managed endpoint is already in artifacts\trusttunnel_endpoint, scripts\build-dev-bundle.ps1
builds the bundle without Docker: it rebuilds only the panel and packs the same archive.

The Windows client is not built here: it lives in its own repository,
https://github.com/divezh/netlane-app, where clients\windows\build.ps1 builds,
signs and packages it.
#>
param(
    [switch]$Panel,
    [switch]$Bundle,
    [switch]$All,
    [switch]$SkipTests
)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$artifacts = Join-Path $root 'artifacts'
$upstreamCommit = 'fab5b8353a19332f935fa30869307d37d4a898d1'
$goImage = 'netlane-local-go'
if ($All) { $Panel = $true; $Bundle = $true }
$checkOnly = -not ($Panel -or $Bundle)

function Invoke-Native([string]$File, [string[]]$Arguments) {
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$File $($Arguments -join ' ') failed with exit code $LASTEXITCODE." }
}
function Test-Quiet([string]$File, [string[]]$Arguments) {
    # Windows PowerShell 5.1 turns redirected native stderr into errors under 'Stop'.
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $File @Arguments *> $null; return ($LASTEXITCODE -eq 0) } catch { return $false } finally { $ErrorActionPreference = $previous }
}
function Find-Tool([string]$Name) {
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    return $null
}
# Installers of NASM, LLVM, Strawberry Perl and rustup do not always update PATH, or a new
# shell is needed; use their default folders for this run.
foreach ($dir in @('C:\Program Files\NASM', 'C:\Program Files\LLVM\bin', 'C:\Strawberry\perl\bin', (Join-Path $env:USERPROFILE '.cargo\bin'))) {
    if ((Test-Path -LiteralPath $dir) -and (($env:PATH -split ';') -notcontains $dir)) { $env:PATH = $dir + ';' + $env:PATH }
}
function Find-Python {
    # The Microsoft Store alias in WindowsApps is not Python: it only offers to install it.
    foreach ($name in 'python', 'py') {
        $command = Get-Command $name -ErrorAction SilentlyContinue
        if ($command -and $command.Source -notlike '*\WindowsApps\*' -and (Test-Quiet $command.Source @('-c', 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)'))) { return $command.Source }
    }
    return $null
}
function Find-Go {
    $go = Find-Tool 'go'
    if ($go) { return $go }
    $portable = Join-Path $root '.tools\go\bin\go.exe'
    if (Test-Path -LiteralPath $portable) { return $portable }
    return $null
}
function Find-VisualStudio {
    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
    if (-not (Test-Path -LiteralPath $vswhere)) { return $null }
    $path = & $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
    if ($path) { return $path.Trim() }
    return $null
}
function Test-Docker {
    if (-not (Find-Tool 'docker')) { return $false }
    return (Test-Quiet 'docker' @('info'))
}
function Write-Artifact([string]$Path) {
    if (Test-Path -LiteralPath $Path) {
        $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
        $size = [Math]::Round((Get-Item -LiteralPath $Path).Length / 1MB, 1)
        Write-Host ("  {0}  {1} MB  sha256 {2}" -f $Path, $size, $hash)
    }
}

# ---- Tool check -------------------------------------------------------------
$go = Find-Go
$csc = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
$hasCsc = Test-Path -LiteralPath $csc
$hasDocker = Test-Docker
$hasGit = [bool](Find-Tool 'git')
$vs = Find-VisualStudio
$native = [ordered]@{
    'Visual Studio Build Tools (C++ x64)' = [bool]$vs
    'Python 3.10+ (not the Store alias)'  = [bool](Find-Python)
    'Conan 2.12 (pip install "conan~=2.12.0")' = [bool](Find-Tool 'conan')
    'GNU make'                            = [bool](Find-Tool 'make')
    'NASM'                                = [bool](Find-Tool 'nasm')
    'Perl (Strawberry)'                   = [bool](Find-Tool 'perl')
    'Rust (rustup)'                       = [bool](Find-Tool 'cargo')
    'clang-format 21 (pip)'               = [bool](Find-Tool 'clang-format')
    'LLVM 21 clangd'                      = [bool](Find-Tool 'clangd')
    'clangd-tidy (pip)'                   = [bool](Find-Tool 'clangd-tidy')
    'jq'                                  = [bool](Find-Tool 'jq')
    'markdownlint (npm)'                  = [bool](Find-Tool 'markdownlint')
}
function Show-State([string]$Name, [bool]$Ok) {
    if ($Ok) { Write-Host "  [ok]      $Name" -ForegroundColor Green } else { Write-Host "  [missing] $Name" -ForegroundColor Yellow }
}
Write-Host 'Build tools'
Show-State 'Git' $hasGit
Show-State 'Go (PATH or .tools\go)' ([bool]$go)
Show-State '.NET Framework 4.8 compiler (csc.exe)' $hasCsc
Show-State 'Docker Desktop, running' $hasDocker
if ($native.Count -gt 0) { foreach ($item in $native.GetEnumerator()) { Show-State $item.Key $item.Value } }
Write-Host ''
Write-Host 'Targets'
Write-Host ("  -Panel         needs Go{0}" -f $(if ($go) { ': ready' } else { ': install Go or keep .tools\go' }))
Write-Host ("  -Bundle        needs Docker Desktop (Linux containers) and Git{0}" -f $(if ($hasDocker -and $hasGit) { ': ready' } else { ': start or install Docker Desktop' }))
if ($checkOnly) {
    Write-Host ''
    Write-Host 'Nothing built. Pass -Panel, -Bundle or -All.'
    return
}
New-Item -ItemType Directory -Force -Path $artifacts | Out-Null
$started = Get-Date
$dockerRepo = "${root}:/repo"

# ---- Panel ------------------------------------------------------------------
function Initialize-GoImage {
    if (-not $script:goImageReady) {
        Invoke-Native 'docker' @('build', '-q', '-t', $goImage, '-f', (Join-Path $root 'deploy\ci\local-go.Dockerfile'), (Join-Path $root 'deploy\ci'))
        $script:goImageReady = $true
    }
}
function Build-Panel {
    if (-not $go) { throw 'Go is missing. Install it from go.dev or restore .tools\go.' }
    if (-not $hasGit) { throw 'Git is required.' }
    Write-Host ''
    Write-Host '== Panel'
    $revision = (& git -C $root rev-parse --short HEAD).Trim()
    if (-not $SkipTests) {
        if ($hasDocker) {
            Initialize-GoImage
            # The same Linux toolchain as CI, with module and build caches kept in Docker volumes.
            Invoke-Native 'docker' @('run', '--rm', '-v', $dockerRepo, '-v', 'netlane-go-mod:/go/pkg/mod', '-v', 'netlane-go-cache:/root/.cache/go-build', '-w', '/repo', $goImage,
                'bash', '-c', 'git config --global --add safe.directory /repo && go vet ./... && go test -race ./...')
        } else {
            Write-Host 'Docker is not running: Go tests run natively on Windows (CI runs them on Linux).' -ForegroundColor Yellow
            Push-Location $root
            try { Invoke-Native $go @('vet', './...'); Invoke-Native $go @('test', './...') } finally { Pop-Location }
        }
    }
    $env:CGO_ENABLED = '0'; $env:GOOS = 'linux'; $env:GOARCH = 'amd64'
    Push-Location $root
    try {
        Invoke-Native $go @('build', '-trimpath', "-ldflags=-s -w -X main.version=local-$revision", '-o', (Join-Path $artifacts 'trusttunnel-admin'), './cmd/trusttunnel-admin')
    } finally {
        Pop-Location
        Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    }
}

# ---- Managed endpoint and install bundle -------------------------------------
function Build-Bundle {
    if (-not ($hasDocker -and $hasGit)) { throw 'The bundle needs Git and a running Docker Desktop with Linux containers.' }
    Write-Host ''
    Write-Host '== Managed endpoint (Rust, Docker)'
    $upstream = Join-Path $root 'research\upstream'
    $patches = Get-ChildItem -LiteralPath (Join-Path $root 'upstream\patches') -Filter '*.patch' | Sort-Object Name
    if (-not (Test-Path -LiteralPath (Join-Path $upstream '.git'))) {
        # LF checkout: the patches and rustfmt expect upstream line endings.
        Invoke-Native 'git' @('-c', 'core.autocrlf=false', 'clone', 'https://github.com/TrustTunnel/TrustTunnel.git', $upstream)
        Invoke-Native 'git' @('-C', $upstream, '-c', 'core.autocrlf=false', 'checkout', '--quiet', $upstreamCommit)
    }
    if ((& git -C $upstream rev-parse HEAD).Trim() -ne $upstreamCommit) { throw "research\upstream is not at the pinned commit $upstreamCommit." }
    $last = $patches[-1].FullName
    if (Test-Quiet 'git' @('-C', $upstream, 'apply', '--reverse', '--check', $last)) {
        Write-Host 'Patches already applied: incremental build.'
    } else {
        $dirty = & git -C $upstream status --porcelain --untracked-files=no
        if ($dirty) { throw 'research\upstream has local changes and the patches are not applied. Clean it or delete the folder.' }
        foreach ($patch in $patches) { Invoke-Native 'git' @('-C', $upstream, 'apply', $patch.FullName) }
    }
    Invoke-Native 'docker' @('build', '-q', '-t', 'tt-upstream-test', '-f', (Join-Path $root 'deploy\ci\upstream.Dockerfile'), (Join-Path $root 'deploy\ci'))
    $cargo = 'cargo build --locked -p trusttunnel_endpoint'
    if (-not $SkipTests) {
        $cargo = 'cargo fmt --all -- --check && cargo clippy --locked -- -D warnings && cargo test --locked -p trusttunnel --lib && cargo test --locked -p trusttunnel --test managed && cargo test --locked -p trusttunnel_endpoint --bin trusttunnel_endpoint && ' + $cargo
    }
    # Target and registry live in Docker volumes: much faster than a Windows bind mount and kept between runs.
    Invoke-Native 'docker' @('run', '--rm', '-v', "${upstream}:/src", '-v', 'netlane-cargo-target:/src/target', '-v', 'netlane-cargo-registry:/usr/local/cargo/registry', '-v', "${artifacts}:/out",
        'tt-upstream-test', 'bash', '-c', "$cargo && cp target/debug/trusttunnel_endpoint /out/trusttunnel_endpoint")

    Write-Host ''
    Write-Host '== Install bundle'
    Initialize-GoImage
    $test = ''
    if (-not $SkipTests) {
        $test = 'CGO_ENABLED=0 go build -o artifacts/trusttunnel-admin ./cmd/trusttunnel-admin && python3 scripts/test-managed-endpoint.py artifacts/trusttunnel_endpoint artifacts/trusttunnel-admin && '
    }
    Invoke-Native 'docker' @('run', '--rm', '-v', $dockerRepo, '-v', 'netlane-go-mod:/go/pkg/mod', '-v', 'netlane-go-cache:/root/.cache/go-build', '-w', '/repo', $goImage,
        'bash', '-c', "git config --global --add safe.directory /repo && $test bash scripts/build-dev-bundle.sh")
}

if ($Panel) { Build-Panel }
if ($Bundle) { Build-Bundle }

Write-Host ''
Write-Host ("Done in {0:mm\:ss}. Artifacts:" -f ((Get-Date) - $started))
foreach ($name in 'trusttunnel-admin', 'trusttunnel_endpoint', 'trusttunnel-dev.tar.gz') { Write-Artifact (Join-Path $artifacts $name) }
if ($Bundle) {
    Write-Host ''
    Write-Host 'Install on a fresh VPS:'
    Write-Host '  powershell -ExecutionPolicy Bypass -File scripts\deploy-vps.ps1 -Target root@IP -Domain vpn.example.com -Bundle artifacts\trusttunnel-dev.tar.gz'
}
