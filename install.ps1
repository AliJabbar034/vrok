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
    # Follow /releases/latest and read the URL it lands on. The tag in that
    # URL avoids the JSON API's unauthenticated rate limit.
    #
    # -MaximumRedirection 0 is the wrong tool for this: PowerShell 7 treats
    # the redirect as a terminating error, and some Windows PowerShell 5.1
    # builds do too, so the install stopped before it downloaded anything.
    $params = @{
        Uri         = "https://github.com/$Repo/releases/latest"
        Method      = 'Head'
        ErrorAction = 'Stop'
    }
    if ($PSVersionTable.PSVersion.Major -lt 6) { $params.UseBasicParsing = $true }
    $response = Invoke-WebRequest @params
    $final = $null
    if ($response.BaseResponse -and $response.BaseResponse.ResponseUri) {
        $final = $response.BaseResponse.ResponseUri.AbsoluteUri
    }
    if (-not $final) {
        $location = $response.Headers['Location']
        if ($location -is [array]) { $location = $location[0] }
        $final = [string]$location
    }
    if ($final -match '/tag/([^/?#]+)') { return $Matches[1] }
    throw "Could not determine the latest version. Set one explicitly: `$env:VROK_VERSION='v0.1.0'"
}

function Publish-EnvironmentChange {
    # Explorer caches the environment from logon. Without this broadcast a
    # terminal opened from the Start menu keeps the PATH from before install.
    if (-not ('Vrok.NativeMethods' -as [type])) {
        Add-Type -Namespace Vrok -Name NativeMethods -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Auto)]
public static extern IntPtr SendMessageTimeout(
    IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam,
    uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
    }
    $result = [UIntPtr]::Zero
    [void][Vrok.NativeMethods]::SendMessageTimeout(
        [IntPtr]0xffff, 0x001A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
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
        # This fails closed: anyone able to block checksums.txt could
        # otherwise also swap the archive. VROK_SKIP_VERIFY=1 opts out, for a
        # mirror trusted without a checksum file.
        Write-Step 'Verifying checksum'
        $expected = $null
        try {
            $sums = Join-Path $tmp 'checksums.txt'
            Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sums -UseBasicParsing
            # Anchor on the archive name. A prefix match would accept
            # vrok_*_windows_amd64.zip.sbom.json and then reject a good zip.
            $line = Select-String -Path $sums -Pattern (' ' + [regex]::Escape($archive) + '\s*$') | Select-Object -First 1
            if ($line) { $expected = ($line.Line -split '\s+')[0].ToLower() }
        } catch {
            $expected = $null
        }
        if (-not $expected) {
            if ($env:VROK_SKIP_VERIFY -eq '1') {
                Write-Warn "Could not verify $archive; installing anyway (VROK_SKIP_VERIFY=1)"
            } else {
                throw "Could not get a checksum for $archive from $base/checksums.txt.`nNot installing an unverified binary. Try again, or set VROK_SKIP_VERIFY=1 if you trust this source."
            }
        } else {
            $actual = (Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLower()
            if ($actual -ne $expected) {
                throw "Checksum mismatch for $archive.`n  expected $expected`n  got      $actual`nNot installing. Please report this."
            }
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
        $installed = Join-Path $InstallDir 'vrok.exe'
        Copy-Item -Path $binary.FullName -Destination $installed -Force
        # A download keeps the Mark of the Web. Leaving it on vrok.exe makes
        # SmartScreen block the program the first time it runs.
        Unblock-File -LiteralPath $installed -ErrorAction SilentlyContinue

        # Add to the *user* PATH, so no elevation is required. The registry is
        # the durable copy; $env:PATH is patched so this session works too.
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if ($userPath -notlike "*$InstallDir*") {
            Write-Step 'Adding vrok to your PATH'
            $updated = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
            [Environment]::SetEnvironmentVariable('Path', $updated, 'User')
            $env:Path = "$env:Path;$InstallDir"
            try { Publish-EnvironmentChange } catch { Write-Warn 'PATH was saved, but Windows was not notified. Quit and reopen your terminal.' }
            $pathChanged = $true
        }

        $reported = & $installed --version 2>$null
        Write-Ok $(if ($reported) { "Installed $reported" } else { "Installed vrok $Version" })
        Write-Host ''
        Write-Host '  ' -NoNewline
        Write-Host $installed -ForegroundColor DarkGray
        if ($pathChanged) {
            # A new tab inherits the terminal app's old environment. Only a
            # full restart of that app picks up the PATH entry.
            Write-Host '  Quit and reopen your terminal, then try:  ' -NoNewline
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
