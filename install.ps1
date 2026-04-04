# Carryon installer for Windows
# Usage: irm https://carryon.dev/get/ps1 | iex
$ErrorActionPreference = "Stop"

$Repo = "carryon-dev/cli"
$Binary = "carryon"

function Write-Info($msg) { Write-Host "> " -ForegroundColor Green -NoNewline; Write-Host $msg }
function Write-Warn($msg) { Write-Host "! " -ForegroundColor Yellow -NoNewline; Write-Host $msg }
function Write-Err($msg) { Write-Host "x " -ForegroundColor Red -NoNewline; Write-Host $msg; exit 1 }

# Detect architecture
$Arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    "X64"   { "amd64" }
    "Arm64" { "arm64" }
    default { Write-Err "Unsupported architecture: $_" }
}

# Get latest version
function Get-LatestVersion {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ "User-Agent" = "carryon-installer" }
    return $release.tag_name
}

# Install directory
$InstallDir = if ($env:CARRYON_INSTALL_DIR) {
    $env:CARRYON_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "carryon\bin"
}

Write-Host ""
Write-Host "  Carryon Installer" -ForegroundColor White
Write-Host "  Terminal sessions that persist" -ForegroundColor DarkGray
Write-Host ""

$Version = if ($env:CARRYON_VERSION) { $env:CARRYON_VERSION } else { Get-LatestVersion }
if (-not $Version) { Write-Err "Could not determine latest version." }

Write-Info "Platform: windows/$Arch"
Write-Info "Version:  $Version"
Write-Info "Target:   $InstallDir\$Binary.exe"

# GoReleaser produces .zip archives: carryon-{version}-windows-{arch}.zip
$BareVersion = $Version -replace '^v', ''
$Archive = "$Binary-$BareVersion-windows-$Arch.zip"
$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$Archive"
$ChecksumsUrl = "https://github.com/$Repo/releases/download/$Version/checksums.txt"

# Download to temp
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) "carryon-install-$(Get-Random)"
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null
$TmpFile = Join-Path $TmpDir $Archive

try {
    Write-Info "Downloading $Archive..."
    Invoke-WebRequest -Uri $DownloadUrl -OutFile $TmpFile -UseBasicParsing

    # Verify checksum
    Write-Info "Verifying checksum..."
    try {
        $ChecksumsFile = Join-Path $TmpDir "checksums.txt"
        Invoke-WebRequest -Uri $ChecksumsUrl -OutFile $ChecksumsFile -UseBasicParsing
        $Checksums = Get-Content $ChecksumsFile
        $ExpectedLine = $Checksums | Where-Object { $_ -match $Archive } | Select-Object -First 1
        if ($ExpectedLine) {
            $Expected = ($ExpectedLine -split '\s+')[0]
            $Actual = (Get-FileHash -Path $TmpFile -Algorithm SHA256).Hash.ToLower()
            if ($Actual -ne $Expected) {
                Write-Err "Checksum mismatch!`n  Expected: $Expected`n  Got:      $Actual"
            }
            Write-Info "Checksum verified"
        } else {
            Write-Warn "Asset not found in checksums - skipping verification"
        }
    } catch {
        Write-Warn "Could not download checksums - skipping verification"
    }

    # Extract and install
    Write-Info "Extracting..."
    Expand-Archive -Path $TmpFile -DestinationPath $TmpDir -Force
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Move-Item -Path (Join-Path $TmpDir "$Binary.exe") -Destination (Join-Path $InstallDir "$Binary.exe") -Force

    # Add to PATH
    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$InstallDir;$UserPath", "User")
        Write-Info "Added $InstallDir to user PATH"
        $env:Path = "$InstallDir;$env:Path"
    }

    # Verify
    Write-Host ""
    try {
        $InstalledVersion = & (Join-Path $InstallDir "$Binary.exe") --version 2>$null
        Write-Host "  carryon $InstalledVersion installed successfully" -ForegroundColor Green
    } catch {
        Write-Host "  carryon installed successfully" -ForegroundColor Green
    }

    Write-Host ""
    Write-Host "  Get started:"
    Write-Host "    carryon --name dev       " -ForegroundColor DarkGray -NoNewline; Write-Host "# create a session"
    Write-Host "    carryon list             " -ForegroundColor DarkGray -NoNewline; Write-Host "# list sessions"
    Write-Host "    carryon --help           " -ForegroundColor DarkGray -NoNewline; Write-Host "# full usage"
    Write-Host ""

} finally {
    Remove-Item -Path $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
