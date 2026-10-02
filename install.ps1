<#
.SYNOPSIS
    Installs vrok on Windows.

.DESCRIPTION
    irm https://raw.githubusercontent.com/AliJabbar034/vrok/main/install.ps1 | iex

    Installs into %LOCALAPPDATA%\Programs\vrok and adds it to the user PATH,
    so no administrator rights are needed.

.PARAMETER Version
    Version to install, e.g. v0.1.0. Defaults to the latest release.

.PARAMETER InstallDir
    Where to put the binaries.
#>

[CmdletBinding()]
param(
    [string] $Version    = $env:VROK_VERSION,
    [string] $InstallDir = $env:VROK_INSTALL_DIR
)

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'   # keeps Invoke-WebRequest fast

$Repo = 'AliJabbar034/vrok'

function Write-Step { param($m) Write-Host "==> " -ForegroundColor Cyan -NoNewline; Write-Host $m }
function Write-Ok   { param($m) Write-Host "OK  " -ForegroundColor Green -NoNewline; Write-Host $m }
function Write-Warn { param($m) Write-Host "!   " -ForegroundColor Yellow -NoNewline; Write-Host $m }

function Get-Architecture {
    # PROCESSOR_ARCHITECTURE reports the shell's architecture, which lies
    # inside a 32-bit host; the OS value is authoritative.
    switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'X64'   { 'amd64' }
        'Arm64' { 'arm64' }
        default {
            throw "vrok does not publish a build for $_. Install from source instead: go install github.com/$Repo/cmd/vrok@latest"
        }
    }
}

function Get-LatestVersion {
    Write-Step 'Finding the latest release'
    # The /releases/latest redirect carries the tag, which avoids the JSON
    # API's unauthenticated rate limit.
    $response = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" `
        -MaximumRedirection 0 -ErrorAction SilentlyContinue -UseBasicParsing
    $location = $response.Headers['Location']
    if (-not $location) {
        throw "Could not determine the latest version. Set one explicitly: `$env:VROK_VERSION='v0.1.0'"
    }
    ($location -split '/')[-1]
}

try {
    if ([Environment]::OSVersion.Version.Major -lt 10) {
        Write-Warn 'Windows 10 or later is recommended: `vrok list` needs Unix socket support, which earlier versions lack. Sharing itself will still work.'
    }

    $arch = Get-Architecture
    if (-not $Version)    { $Version = Get-LatestVersion }
    if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\vrok' }

    $archive = "vrok_${Version}_windows_${arch}.zip"
    $base    = "https://github.com/$Repo/releases/download/$Version"
    $tmp     = Join-Path ([System.IO.Path]::GetTempPath()) "vrok-$([guid]::NewGuid())"

    New-Item -ItemType Directory -Path $tmp -Force | Out-Null
    try {
        Write-Step "Downloading vrok $Version for windows/$arch"
        $zip = Join-Path $tmp $archive
        try {
            Invoke-WebRequest -Uri "$base/$archive" -OutFile $zip -UseBasicParsing
        } catch {
            throw "Could not download $base/$archive`nCheck that $Version exists and publishes a windows_$arch build."
        }

        # Verify the download before it becomes an executable on the PATH.
        try {
            $sums = Join-Path $tmp 'checksums.txt'
            Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sums -UseBasicParsing
            Write-Step 'Verifying checksum'
            $line = Select-String -Path $sums -Pattern ([regex]::Escape($archive)) | Select-Object -First 1
            if ($line) {
                $expected = ($line.Line -split '\s+')[0]
                $actual   = (Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLower()
                if ($actual -ne $expected.ToLower()) {
                    throw "Checksum mismatch for $archive.`n  expected $expected`n  got      $actual`nNot installing. Please report this."
                }
            } else {
                Write-Warn "No checksum listed for $archive; skipping verification"
            }
        } catch [System.Net.WebException] {
            Write-Warn 'Could not fetch checksums.txt; skipping verification'
        }

        Write-Step 'Extracting'
        Expand-Archive -Path $zip -DestinationPath $tmp -Force
        $binary = Get-ChildItem -Path $tmp -Recurse -Filter 'vrok.exe' | Select-Object -First 1
        if (-not $binary) { throw 'The archive did not contain vrok.exe.' }

        # Only the CLI is installed. vrok-relay is a server that belongs on a
        # host with a wildcard DNS record, not on a laptop's PATH; it ships in
        # the archive and as a container image for anyone who wants it.
        Write-Step "Installing to $InstallDir"
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        Copy-Item -Path $binary.FullName -Destination $InstallDir -Force

        # Add to the *user* PATH, so no elevation is required. The registry is
        # the durable copy; $env:PATH is patched so this session works too.
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if ($userPath -notlike "*$InstallDir*") {
            Write-Step 'Adding vrok to your PATH'
            $updated = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
            [Environment]::SetEnvironmentVariable('Path', $updated, 'User')
            $env:Path = "$env:Path;$InstallDir"
            $pathChanged = $true
        }

        $reported = & (Join-Path $InstallDir 'vrok.exe') --version 2>$null
        Write-Ok $(if ($reported) { "Installed $reported" } else { "Installed vrok $Version" })
        Write-Host ''
        if ($pathChanged) {
            Write-Host '  Open a new terminal, then try:  ' -NoNewline
            Write-Host 'vrok .\some-file' -ForegroundColor DarkGray
        } else {
            Write-Host '  Try it:  ' -NoNewline
            Write-Host 'vrok .\some-file' -ForegroundColor DarkGray
        }
    } finally {
        Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
} catch {
    Write-Host "X   $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
