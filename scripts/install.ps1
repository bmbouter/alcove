# Alcove CLI Installation Script for Windows
#
# Usage (Admin PowerShell): iex (iwr https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1).Content
# Usage (User PowerShell):  iex (iwr https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1).Content
#
# This script automatically installs the Alcove CLI for Windows.
#
# Environment variables:
#   $env:INSTALL_DIR     - Installation directory (default: C:\Program Files\Alcove or $HOME\bin if no admin)
#   $env:ALCOVE_VERSION  - Specific version to install (default: latest release)
#   $env:SKIP_CHECKSUM   - Skip checksum verification (not recommended)
#

param(
    [string]$Version = "",
    [string]$InstallDir = "",
    [switch]$SkipChecksum
)

$ErrorActionPreference = "Stop"

# Configuration
$Repo = "bmbouter/alcove"
$BinaryName = "alcove"
$GitHubApiUrl = "https://api.github.com/repos/$Repo"
$DownloadUrlPrefix = "https://github.com/$Repo/releases/download"

# Colors for output
function Write-Info($Message) {
    Write-Host "[INFO] $Message" -ForegroundColor Blue
}

function Write-Success($Message) {
    Write-Host "[SUCCESS] $Message" -ForegroundColor Green
}

function Write-Warning($Message) {
    Write-Host "[WARN] $Message" -ForegroundColor Yellow
}

function Write-Error($Message) {
    Write-Host "[ERROR] $Message" -ForegroundColor Red
    exit 1
}

# Detect architecture
function Get-Architecture {
    $arch = $env:PROCESSOR_ARCHITECTURE
    switch ($arch) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        default {
            Write-Error "Unsupported architecture: $arch"
        }
    }
}

# Get latest version from GitHub API
function Get-LatestVersion {
    Write-Info "Fetching latest release information..."
    try {
        $response = Invoke-RestMethod -Uri "$GitHubApiUrl/releases/latest"
        return $response.tag_name
    }
    catch {
        Write-Error "Failed to fetch latest version from GitHub API: $($_.Exception.Message)"
    }
}

# Download file
function Download-File($Url, $OutputPath) {
    Write-Info "Downloading from $Url..."
    try {
        Invoke-WebRequest -Uri $Url -OutFile $OutputPath -UseBasicParsing
    }
    catch {
        Write-Error "Failed to download $Url`: $($_.Exception.Message)"
    }
}

# Verify checksum
function Test-Checksum($FilePath, $ChecksumFile) {
    if ($SkipChecksum -or $env:SKIP_CHECKSUM -eq "true") {
        Write-Warning "Skipping checksum verification"
        return
    }

    Write-Info "Verifying checksum..."

    $fileName = [System.IO.Path]::GetFileName($FilePath)
    $checksumContent = Get-Content $ChecksumFile
    $expectedLine = $checksumContent | Where-Object { $_ -match [regex]::Escape($fileName) }

    if (-not $expectedLine) {
        Write-Error "Checksum for $fileName not found in checksums file"
    }

    $expected = ($expectedLine -split '\s+')[0]
    $actual = (Get-FileHash -Path $FilePath -Algorithm SHA256).Hash.ToLower()

    if ($expected -ne $actual) {
        Write-Error "Checksum verification failed!`nExpected: $expected`nActual:   $actual"
    }

    Write-Success "Checksum verified"
}

# Install binary
function Install-Binary($BinaryPath, $InstallPath) {
    Write-Info "Installing $BinaryName to $InstallPath..."

    try {
        # Create directory if it doesn't exist
        $installDir = [System.IO.Path]::GetDirectoryName($InstallPath)
        if (-not (Test-Path $installDir)) {
            New-Item -ItemType Directory -Path $installDir -Force | Out-Null
        }

        Move-Item -Path $BinaryPath -Destination $InstallPath -Force
        Write-Success "$BinaryName installed to $InstallPath"
    }
    catch {
        Write-Error "Failed to install binary: $($_.Exception.Message)"
    }
}

# Update PATH if needed
function Update-Path($InstallDir) {
    $currentPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ($currentPath -notlike "*$InstallDir*") {
        Write-Warning "$InstallDir is not in your PATH"
        Write-Host ""
        Write-Host "To add it to your PATH:"
        Write-Host "1. Open System Properties > Environment Variables"
        Write-Host "2. Edit the PATH variable for your user"
        Write-Host "3. Add: $InstallDir"
        Write-Host ""
        Write-Host "Or use the full path: $InstallDir\$BinaryName.exe"
        Write-Host ""
        Write-Host "Alternatively, run this command to update PATH for current user:"
        Write-Host "  `$env:PATH += `";$InstallDir`""
        Write-Host "  [Environment]::SetEnvironmentVariable(`"PATH`", `$env:PATH, `"User`")"
    }
}

# Main installation function
function Main {
    Write-Host "=== Alcove CLI Installer for Windows ===" -ForegroundColor Cyan
    Write-Host ""

    # Detect architecture
    $arch = Get-Architecture
    Write-Info "Detected architecture: $arch"

    # Get version
    $versionToInstall = $Version
    if (-not $versionToInstall) {
        $versionToInstall = $env:ALCOVE_VERSION
    }
    if (-not $versionToInstall) {
        $versionToInstall = Get-LatestVersion
    }
    Write-Info "Installing version: $versionToInstall"

    # Determine install directory
    $installDirectory = $InstallDir
    if (-not $installDirectory) {
        $installDirectory = $env:INSTALL_DIR
    }
    if (-not $installDirectory) {
        # Check if running as administrator
        $isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole] "Administrator")

        if ($isAdmin) {
            $installDirectory = "C:\Program Files\Alcove"
        } else {
            $installDirectory = "$env:USERPROFILE\bin"
        }
    }

    Write-Info "Installation directory: $installDirectory"

    # Prepare download URLs
    $binaryName = "$BinaryName-windows-$arch.exe"
    $downloadUrl = "$DownloadUrlPrefix/$versionToInstall/$binaryName"
    $checksumUrl = "$DownloadUrlPrefix/$versionToInstall/checksums-sha256.txt"

    # Create temporary directory
    $tempDir = [System.IO.Path]::GetTempPath() + [System.Guid]::NewGuid().ToString()
    New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

    try {
        $binaryFile = Join-Path $tempDir $binaryName
        $checksumFile = Join-Path $tempDir "checksums-sha256.txt"

        # Download files
        Download-File $downloadUrl $binaryFile
        Download-File $checksumUrl $checksumFile

        # Verify checksum
        Test-Checksum $binaryFile $checksumFile

        # Install
        $installPath = Join-Path $installDirectory "$BinaryName.exe"
        Install-Binary $binaryFile $installPath

        # Update PATH guidance
        Update-Path $installDirectory

        Write-Host ""
        Write-Success "Installation complete!" -ForegroundColor Green
        Write-Host ""
        Write-Host "Verify your installation:" -ForegroundColor Yellow
        Write-Host "  $BinaryName version"
        Write-Host ""
        Write-Host "Get started:" -ForegroundColor Yellow
        Write-Host "  $BinaryName --help"
        Write-Host "  $BinaryName login https://your-bridge-instance.com"
        Write-Host ""
    }
    finally {
        # Cleanup
        if (Test-Path $tempDir) {
            Remove-Item -Path $tempDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

# Run main installation
Main