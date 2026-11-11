<#
.SYNOPSIS
Fast Netlane install on a fresh VPS from Windows 10/11.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File scripts\deploy-vps.ps1 -Target root@203.0.113.10 -Domain vpn.example.com

.DESCRIPTION
Downloads the newest successful CI install bundle of main with the GitHub CLI (run
"gh auth login" once; the repository is private) or uses -Bundle, checks the checksum and
DNS, uploads the bundle with the built-in OpenSSH client and runs the installer. The panel
administrator password is generated on the VPS and shown once at the end.
#>
param(
    [Parameter(Mandatory = $true)][string]$Target,
    [Parameter(Mandatory = $true)][string]$Domain,
    [string]$Bundle,
    [string]$Key,
    [switch]$NoFirewall,
    [switch]$NoKey,
    [switch]$SkipDnsCheck
)
$ErrorActionPreference = 'Stop'
$repo = 'divezh/netlane'
$Domain = $Domain.ToLowerInvariant()
if ($Target -notmatch '^[^@\s]+@[^@\s]+$') { throw 'Target must look like root@203.0.113.10.' }
if ($Domain -notmatch '^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}$') { throw "Invalid domain: $Domain" }
foreach ($tool in 'ssh', 'scp') {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { throw "$tool not found. Enable the Windows OpenSSH Client feature." }
}
$work = Join-Path ([IO.Path]::GetTempPath()) ('netlane-deploy-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    Write-Host '1/4 Install bundle'
    $archive = Join-Path $work 'trusttunnel-dev.tar.gz'
    $checksum = "$archive.sha256"
    if ($Bundle) {
        if (-not (Test-Path -LiteralPath $Bundle) -or -not (Test-Path -LiteralPath "$Bundle.sha256")) { throw "Need $Bundle and $Bundle.sha256." }
        Copy-Item -LiteralPath $Bundle -Destination $archive
        Copy-Item -LiteralPath "$Bundle.sha256" -Destination $checksum
    } else {
        if (-not (Get-Command gh -ErrorAction SilentlyContinue)) { throw 'Install the GitHub CLI (winget install GitHub.cli), run "gh auth login", or pass -Bundle.' }
        $tag = (& gh release view -R $repo --json tagName -q .tagName)
        if ($LASTEXITCODE -ne 0 -or -not $tag) { throw 'No release was found (is gh logged in?).' }
        Write-Host "    release $tag"
        & gh release download $tag -R $repo -p 'trusttunnel-dev.tar.gz' -p 'trusttunnel-dev.tar.gz.sha256' -D $work
        if ($LASTEXITCODE -ne 0) { throw 'Bundle download failed.' }
    }
    $expected = ((Get-Content -LiteralPath $checksum -Raw).Trim() -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { throw 'Bundle checksum mismatch: download it again.' }
    Write-Host '    checksum OK'

    Write-Host '2/4 DNS'
    $hostPart = $Target.Split('@')[1]
    function Get-IPv4([string]$name) {
        try { @([Net.Dns]::GetHostAddresses($name) | Where-Object { $_.AddressFamily -eq 'InterNetwork' } | ForEach-Object { $_.IPAddressToString }) }
        catch { @() }
    }
    $hostIps = Get-IPv4 $hostPart
    $domainIps = Get-IPv4 $Domain
    $match = @($domainIps | Where-Object { $hostIps -contains $_ }).Count -gt 0
    if (-not $match) {
        if (-not $SkipDnsCheck) {
            throw ("$Domain -> " + ($(if ($domainIps.Count) { $domainIps -join ', ' } else { 'nothing' })) + ", VPS -> " + ($hostIps -join ', ') +
                '. Create an A record for the domain pointing to the VPS (Cloudflare: DNS only), wait and retry. Use -SkipDnsCheck only if your resolver is wrong.')
        }
        Write-Host '    DNS check skipped'
    } else {
        Write-Host ("    $Domain -> " + ($domainIps -join ', '))
    }

    $uploads = @($archive, $checksum, (Join-Path $PSScriptRoot 'netlane-remote-install.sh'))
    if (-not $NoKey) {
        if (-not $Key) {
            foreach ($candidate in "$HOME\.ssh\id_ed25519.pub", "$HOME\.ssh\id_rsa.pub") {
                if (Test-Path -LiteralPath $candidate) { $Key = $candidate; break }
            }
        }
        if ($Key -and (Test-Path -LiteralPath $Key)) {
            $keyCopy = Join-Path $work 'netlane-authorized-key.pub'
            Copy-Item -LiteralPath $Key -Destination $keyCopy
            $uploads += $keyCopy
        } else {
            Write-Host '    No SSH public key found; create one with "ssh-keygen -t ed25519" to stop using passwords.'
        }
    }

    Write-Host "3/4 Upload to $Target (SSH may ask for the password)"
    & scp -q @uploads "${Target}:"
    if ($LASTEXITCODE -ne 0) { throw 'Upload failed.' }

    Write-Host '4/4 Install (2-4 minutes)'
    $arguments = $Domain
    if ($NoFirewall) { $arguments += ' --no-firewall' }
    # No double quotes inside: Windows PowerShell 5 drops them when calling native programs.
    $remote = 'if [ $(id -u) = 0 ]; then bash ~/netlane-remote-install.sh ' + $arguments + '; else sudo bash ~/netlane-remote-install.sh ' + $arguments + '; fi'
    & ssh -t $Target $remote
    if ($LASTEXITCODE -ne 0) { throw 'Installation failed; the output above names the reason. The bundle stays on the VPS for a retry.' }
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
