# Alcove CLI Installation Guide

This guide covers all methods for installing the Alcove CLI on different platforms.

## Quick Installation

### Linux and macOS

```bash
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
```

### Windows (PowerShell)

```powershell
iex (iwr -useb 'https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1').Content
```

## Manual Installation

### Download Binaries

Visit the [GitHub Releases page](https://github.com/bmbouter/alcove/releases/latest) and download the appropriate binary for your platform:

| Platform | Architecture | Binary Name |
|----------|--------------|-------------|
| Linux | AMD64 (x86_64) | `alcove-linux-amd64` |
| Linux | ARM64 (AArch64) | `alcove-linux-arm64` |
| macOS | Intel (AMD64) | `alcove-darwin-amd64` |
| macOS | Apple Silicon (ARM64) | `alcove-darwin-arm64` |
| Windows | AMD64 (x86_64) | `alcove-windows-amd64.exe` |

### Install on Linux/macOS

1. Download the binary:
   ```bash
   # Linux AMD64
   wget https://github.com/bmbouter/alcove/releases/latest/download/alcove-linux-amd64

   # macOS ARM64 (Apple Silicon)
   wget https://github.com/bmbouter/alcove/releases/latest/download/alcove-darwin-arm64
   ```

2. Make it executable:
   ```bash
   chmod +x alcove-*
   ```

3. Move to a directory in your PATH:
   ```bash
   # System-wide installation (requires sudo)
   sudo mv alcove-* /usr/local/bin/alcove

   # User installation
   mkdir -p ~/bin
   mv alcove-* ~/bin/alcove
   export PATH="$HOME/bin:$PATH"  # Add to your shell profile
   ```

### Install on Windows

1. Download `alcove-windows-amd64.exe` from the releases page
2. Rename it to `alcove.exe`
3. Place it in a directory in your PATH, such as:
   - `C:\Windows\System32` (system-wide, requires admin)
   - `%USERPROFILE%\bin` (user-specific)
   - Create a new directory and add it to your PATH

## Custom Installation Directory

### Linux/macOS

Set the `INSTALL_DIR` environment variable before running the installation script:

```bash
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | INSTALL_DIR="$HOME/bin" bash
```

Common installation directories:
- `/usr/local/bin` (default, system-wide)
- `$HOME/bin` (user-specific)
- `$HOME/.local/bin` (XDG standard)

### Windows

Set the `INSTALL_DIR` environment variable:

```powershell
$env:INSTALL_DIR = "C:\tools"
iex (iwr -useb 'https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1').Content
```

## Verification

After installation, verify that the CLI is working:

```bash
# Check version
alcove version

# Show help
alcove --help

# Test configuration validation (will show errors if not logged in yet)
alcove config validate
```

## Security Verification

The installation scripts automatically verify checksums when possible. For manual verification:

1. Download the checksums file:
   ```bash
   wget https://github.com/bmbouter/alcove/releases/latest/download/checksums-sha256.txt
   ```

2. Verify your binary:
   ```bash
   sha256sum -c checksums-sha256.txt 2>/dev/null | grep alcove-linux-amd64
   ```

## Troubleshooting

### Permission Denied

If you get "permission denied" errors:

**Linux/macOS:**
- Use `sudo` for system-wide installation, or
- Install to a user directory like `$HOME/bin`
- Ensure the binary has execute permissions: `chmod +x alcove`

**Windows:**
- Run PowerShell as Administrator for system-wide installation, or
- Install to a user directory like `%USERPROFILE%\bin`

### Command Not Found

If the `alcove` command is not found after installation:

1. Check if the installation directory is in your PATH:
   ```bash
   echo $PATH  # Linux/macOS
   echo $env:PATH  # Windows PowerShell
   ```

2. Add the installation directory to your PATH:
   ```bash
   # Add to ~/.bashrc, ~/.zshrc, etc.
   export PATH="$HOME/bin:$PATH"
   ```

   ```powershell
   # Windows (permanent)
   [Environment]::SetEnvironmentVariable('PATH', $env:PATH + ';C:\your\install\dir', 'User')
   ```

### Download Failures

If downloads fail:

1. **Network issues**: Check your internet connection and any corporate firewalls
2. **Certificate issues**: Try adding `-k` flag to curl: `curl -fsSLk ...`
3. **Rate limiting**: Wait a few minutes and try again
4. **Manual download**: Download directly from the GitHub releases page in a browser

### Checksum Verification Failures

If checksum verification fails:

1. **Corrupted download**: Try downloading again
2. **Wrong binary**: Ensure you downloaded the correct platform binary
3. **Manual verification**: Download the checksums file and verify manually

## Using the CLI

Once installed, you can start using the CLI:

### Initial Setup

1. **Connect to a Bridge instance:**
   ```bash
   alcove login https://your-bridge-instance.com
   ```

2. **Verify configuration:**
   ```bash
   alcove config validate
   ```

### Basic Usage

1. **Submit a task:**
   ```bash
   alcove run "Fix the bug in the authentication module"
   ```

2. **List sessions:**
   ```bash
   alcove list --since 24h
   ```

3. **View session logs:**
   ```bash
   alcove logs <session-id>
   ```

4. **Follow logs in real-time:**
   ```bash
   alcove logs -f <session-id>
   ```

For complete command reference, see [CLI Reference](cli-reference.md).

## Updating the CLI

To update to the latest version, simply run the installation script again:

```bash
# Linux/macOS
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash

# Windows
iex (iwr -useb 'https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1').Content
```

The installer will automatically replace the existing binary with the latest version.

## Uninstallation

To remove the Alcove CLI:

1. **Remove the binary:**
   ```bash
   # If installed to /usr/local/bin (default)
   sudo rm /usr/local/bin/alcove

   # If installed to ~/bin
   rm ~/bin/alcove

   # Windows
   del "%USERPROFILE%\bin\alcove.exe"
   ```

2. **Remove configuration (optional):**
   ```bash
   # Linux/macOS
   rm -rf ~/.config/alcove

   # Windows
   rmdir /s "%APPDATA%\alcove"
   ```

## Package Managers (Future)

Package manager support is planned for future releases:

- **Homebrew** (macOS/Linux): `brew install alcove`
- **Chocolatey** (Windows): `choco install alcove`
- **Scoop** (Windows): `scoop install alcove`
- **APT/Snap** (Ubuntu): `apt install alcove`

## Building from Source

If you prefer to build from source:

```bash
git clone https://github.com/bmbouter/alcove.git
cd alcove
make build-cli-all
```

This will create binaries for all platforms in the `dist/` directory.
