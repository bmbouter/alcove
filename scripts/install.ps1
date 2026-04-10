# Alcove CLI Installation Script for Windows
#
# This script installs the Alcove CLI by downloading the appropriate binary
# for Windows from the latest GitHub release.
#
# Usage:
#   PowerShell: 
#     iex (iwr -useb 'https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1').Content
#   
#   Or with custom installation directory:
#     $env:INSTALL_DIR = "C:\tools"; iex (iwr -useb 'https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1').Content

param(
    [string]$InstallDir = $env:INSTALL_DIR
)

# Configuration
$Repo = "bmbouter/alcove"
$BinaryName = "alcove.exe"
$GitHubUrl = "https://api.github.com/repos/$Repo"

# Default install directory
if (-not $InstallDir) {
    $InstallDir = "$env:USERPROFILE\bin"
}

# Colors and output functions
function Write-Info {
    param([string]$Message)
    Write-Host "[INFO] $Message" -ForegroundColor Blue
}

function Write-Success {
    param([string]$Message)
    Write-Host "[SUCCESS] $Message" -ForegroundColor Green
}

function Write-Warning {
    param([string]$Message)
    Write-Host "[WARNING] $Message" -ForegroundColor Yellow
}

function Write-Error {
    param([string]$Message)
    Write-Host "[ERROR] $Message" -ForegroundColor Red
    exit 1
}

# Get latest release info
function Get-LatestRelease {
    Write-Info "Fetching latest release information..."
    
    try {
        $response = Invoke-RestMethod -Uri "$GitHubUrl/releases/latest" -UseBasicParsing
        return $response
    }
    catch {
        Write-Error "Failed to fetch release information: $($_.Exception.Message)"
    }
}

# Download and verify binary
function Download-Binary {
    param(
        [string]$Version,
        [string]$DownloadUrl,
        [string]$ChecksumsUrl
    )
    
    $tempDir = [System.IO.Path]::GetTempPath()
    $binaryFile = Join-Path $tempDir "alcove-windows-amd64.exe"
    $checksumsFile = Join-Path $tempDir "checksums-sha256.txt"
    
    Write-Info "Downloading alcove $Version for Windows..."
    
    try {
        # Download binary
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $binaryFile -UseBasicParsing
        
        # Download checksums
        try {
            Invoke-WebRequest -Uri $ChecksumsUrl -OutFile $checksumsFile -UseBasicParsing
            
            # Verify checksum
            Write-Info "Verifying checksum..."
            $checksumContent = Get-Content $checksumsFile
            $expectedLine = $checksumContent | Where-Object { $_ -match "alcove-windows-amd64\.exe" }
            
            if ($expectedLine) {
                $expectedChecksum = ($expectedLine -split "\s+")[0]
                $actualChecksum = (Get-FileHash -Path $binaryFile -Algorithm SHA256).Hash.ToLower()
                
                if ($actualChecksum -eq $expectedChecksum) {
                    Write-Success "Checksum verified successfully"
                } else {
                    Write-Error "Checksum verification failed! Expected: $expectedChecksum, Got: $actualChecksum"
                }
            } else {
                Write-Warning "Checksum not found for Windows platform"
            }
        }
        catch {
            Write-Warning "Failed to download or verify checksums: $($_.Exception.Message)"
        }
    }
    catch {
        Write-Error "Failed to download binary: $($_.Exception.Message)"
    }
    
    return $binaryFile
}

# Install binary
function Install-Binary {
    param([string]$BinaryFile)
    
    $installPath = Join-Path $InstallDir $BinaryName
    
    # Create install directory if it doesn't exist
    if (-not (Test-Path $InstallDir)) {
        Write-Info "Creating installation directory: $InstallDir"
        try {
            New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        }
        catch {
            Write-Error "Failed to create $InstallDir. Error: $($_.Exception.Message)"
        }
    }
    
    Write-Info "Installing alcove to $installPath..."
    
    try {
        Copy-Item $BinaryFile $installPath -Force
        Write-Success "alcove installed successfully to $installPath"
    }
    catch {
        Write-Error "Failed to install binary: $($_.Exception.Message)"
    }
    
    # Check if install directory is in PATH
    $pathDirs = $env:PATH -split ";"
    if ($InstallDir -notin $pathDirs) {
        Write-Warning "$InstallDir is not in your PATH."
        Write-Host "To add it permanently, run:" -ForegroundColor Cyan
        Write-Host "  [Environment]::SetEnvironmentVariable('PATH', `$env:PATH + ';$InstallDir', 'User')" -ForegroundColor Cyan
        Write-Host ""
        Write-Host "Or run with full path: $installPath" -ForegroundColor Cyan
    }
    
    return $installPath
}

# Test installation
function Test-Installation {
    param([string]$InstallPath)
    
    Write-Info "Testing installation..."
    
    try {
        $versionOutput = & $InstallPath version 2>$null
        if ($LASTEXITCODE -eq 0) {
            Write-Success "Installation test passed: $versionOutput"
        } else {
            Write-Warning "Installation test failed. The binary may not be working correctly."
            Write-Host "Try running: $InstallPath --help"
        }
    }
    catch {
        Write-Warning "Installation test failed: $($_.Exception.Message)"
        Write-Host "Try running: $InstallPath --help"
    }
}

# Main installation flow
function Main {
    Write-Host ""
    Write-Host "🚀 Alcove CLI Installer for Windows" -ForegroundColor Cyan
    Write-Host "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Cyan
    Write-Host ""
    
    Write-Info "Detected platform: Windows (amd64)"
    
    # Get release information
    $release = Get-LatestRelease
    $version = $release.tag_name
    
    if (-not $version) {
        Write-Error "Failed to parse latest version from GitHub API"
    }
    
    Write-Info "Latest version: $version"
    
    # Construct download URLs
    $downloadUrl = "https://github.com/$Repo/releases/download/$version/alcove-windows-amd64.exe"
    $checksumsUrl = "https://github.com/$Repo/releases/download/$version/checksums-sha256.txt"
    
    # Download and verify
    $binaryFile = Download-Binary -Version $version -DownloadUrl $downloadUrl -ChecksumsUrl $checksumsUrl
    
    # Install
    $installPath = Install-Binary -BinaryFile $binaryFile
    
    # Test
    Test-Installation -InstallPath $installPath
    
    Write-Host ""
    Write-Success "Alcove CLI installation complete!"
    Write-Host ""
    Write-Host "Next steps:" -ForegroundColor Cyan
    Write-Host "  1. Connect to your Bridge instance:" -ForegroundColor White
    Write-Host "     alcove login https://your-bridge-instance.com" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  2. Submit a task:" -ForegroundColor White
    Write-Host "     alcove run `"Fix the bug in the login function`"" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  3. Get help:" -ForegroundColor White
    Write-Host "     alcove --help" -ForegroundColor Gray
    Write-Host ""
    
    # Cleanup
    try {
        Remove-Item $binaryFile -Force -ErrorAction SilentlyContinue
    }
    catch {
        # Ignore cleanup errors
    }
}

# Run installer
Main
