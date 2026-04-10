# Alcove CLI Installation Guide

The Alcove CLI provides a command-line interface to interact with Alcove Bridge instances. This guide covers various installation methods across different platforms.

## Quick Installation

### Linux and macOS

Use the one-line installer script:

```bash
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
```

This script automatically detects your platform and architecture, downloads the appropriate binary, verifies checksums, and installs it to your system.

### Windows

Use PowerShell (run as Administrator for system-wide installation):

```powershell
iex (iwr https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1).Content
```

## Manual Installation

### 1. Download Binary

Download the appropriate binary for your platform from the [latest release](https://github.com/bmbouter/alcove/releases/latest):

| Platform | Architecture | Binary Name |
|----------|--------------|-------------|
| Linux | AMD64 | `alcove-linux-amd64` |
| Linux | ARM64 | `alcove-linux-arm64` |
| macOS | Intel (AMD64) | `alcove-darwin-amd64` |
| macOS | Apple Silicon (ARM64) | `alcove-darwin-arm64` |
| Windows | AMD64 | `alcove-windows-amd64.exe` |

### 2. Verify Checksum (Recommended)

Download the `checksums-sha256.txt` file from the same release and verify:

```bash
# Linux/macOS
sha256sum -c checksums-sha256.txt --ignore-missing

# macOS alternative
shasum -a 256 -c checksums-sha256.txt --ignore-missing

# Windows PowerShell
$hash = (Get-FileHash -Algorithm SHA256 alcove-windows-amd64.exe).Hash.ToLower()
$expected = (Get-Content checksums-sha256.txt | Select-String "alcove-windows-amd64.exe").Line.Split()[0]
if ($hash -eq $expected) { Write-Host "Checksum verified" } else { Write-Host "Checksum mismatch!" }
```

### 3. Install Binary

#### Linux/macOS

```bash
# Make executable
chmod +x alcove-*

# Move to PATH (choose one):
sudo mv alcove-* /usr/local/bin/alcove          # System-wide
mv alcove-* ~/.local/bin/alcove                 # User-only (ensure ~/.local/bin is in PATH)
mkdir -p ~/bin && mv alcove-* ~/bin/alcove      # User-only alternative
```

#### Windows

```powershell
# Create directory and move binary
New-Item -ItemType Directory -Force -Path "C:\Program Files\Alcove"
Move-Item alcove-windows-amd64.exe "C:\Program Files\Alcove\alcove.exe"

# Add to PATH (requires restart or new terminal)
$path = [Environment]::GetEnvironmentVariable("PATH", "Machine")
[Environment]::SetEnvironmentVariable("PATH", "$path;C:\Program Files\Alcove", "Machine")
```

## Installation Options

### Environment Variables

The installation scripts support several environment variables:

- `INSTALL_DIR` - Custom installation directory
- `ALCOVE_VERSION` - Install specific version (default: latest)
- `SKIP_CHECKSUM` - Skip checksum verification (not recommended)

Examples:

```bash
# Install to custom directory
INSTALL_DIR="$HOME/.local/bin" curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash

# Install specific version
ALCOVE_VERSION="v1.2.3" curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
```

### Offline Installation

For environments without internet access:

1. Download binaries and scripts on a connected machine
2. Transfer files to target machine
3. Run installation script locally:

```bash
# After transferring files
chmod +x install.sh
./install.sh
```

## Verification

After installation, verify it works:

```bash
# Check version
alcove version

# Show help
alcove --help

# Test connectivity (requires Bridge instance)
alcove login https://your-bridge-instance.com
```

## Getting Started

### Configuration

The CLI can be configured in three ways (in priority order):

1. **Command-line flags**: `--server https://bridge.example.com`
2. **Environment variable**: `export ALCOVE_SERVER=https://bridge.example.com`
3. **Config file**: `~/.config/alcove/config.yaml`

### First Steps

```bash
# Authenticate to your Bridge instance
alcove login https://your-bridge-instance.com

# Submit a task
alcove run "Add unit tests for the user authentication module"

# List recent sessions
alcove list --since 24h

# Get detailed status
alcove status <session-id>

# Stream logs in real-time
alcove logs <session-id> --follow

# Cancel a running session
alcove cancel <session-id>
```

### Configuration Management

```bash
# Validate current configuration
alcove config validate

# View help for all commands
alcove --help
alcove <command> --help
```

## Troubleshooting

### Common Issues

#### Binary not found after installation

- **Linux/macOS**: Ensure installation directory is in your `PATH`:
  ```bash
  echo $PATH | tr ':' '\n' | grep -E '(local/bin|/bin)'
  ```
  Add to `~/.bashrc` or `~/.zshrc`:
  ```bash
  export PATH="$PATH:/usr/local/bin"
  ```

- **Windows**: Restart terminal or add installation directory to PATH manually.

#### Permission denied errors

- **Linux/macOS**: Use `sudo` for system-wide installation or choose user directory:
  ```bash
  INSTALL_DIR="$HOME/bin" curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
  ```

- **Windows**: Run PowerShell as Administrator for system-wide installation.

#### SSL/TLS certificate errors

If you encounter certificate errors:

```bash
# Temporary workaround (not recommended for production)
SKIP_CHECKSUM=true curl -k -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
```

#### Architecture detection issues

For unsupported architectures, download manually:

```bash
# Check your architecture
uname -m

# Download specific binary
curl -fsSL -o alcove https://github.com/bmbouter/alcove/releases/latest/download/alcove-linux-amd64
chmod +x alcove
sudo mv alcove /usr/local/bin/
```

### Getting Help

- **CLI help**: `alcove --help` or `alcove <command> --help`
- **Configuration issues**: `alcove config validate`
- **Verbose output**: Most commands support `--output json` for detailed information
- **GitHub Issues**: [bmbouter/alcove/issues](https://github.com/bmbouter/alcove/issues)

## Uninstallation

To remove the CLI:

```bash
# Linux/macOS
which alcove  # Find installation location
sudo rm $(which alcove)

# Windows
Remove-Item "C:\Program Files\Alcove\alcove.exe"
# Remove from PATH through System Properties > Environment Variables
```

Remove configuration files:

```bash
# Linux/macOS
rm -rf ~/.config/alcove

# Windows
Remove-Item -Recurse "$env:USERPROFILE\.config\alcove"
```

## Building from Source

If you prefer to build from source:

```bash
# Prerequisites: Go 1.25+
git clone https://github.com/bmbouter/alcove.git
cd alcove

# Build for current platform
make build
sudo cp bin/alcove /usr/local/bin/

# Or build for all platforms
make build-cli-all
ls dist/alcove-*
```