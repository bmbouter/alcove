#!/bin/bash
#
# Alcove CLI Installation Script
#
# Usage: curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
#
# This script automatically detects your platform and installs the Alcove CLI.
#
# Environment variables:
#   INSTALL_DIR   - Installation directory (default: /usr/local/bin or ~/bin if no sudo)
#   ALCOVE_VERSION - Specific version to install (default: latest release)
#   SKIP_CHECKSUM  - Skip checksum verification (not recommended)
#

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
REPO="bmbouter/alcove"
BINARY_NAME="alcove"
GITHUB_API_URL="https://api.github.com/repos/${REPO}"
DOWNLOAD_URL_PREFIX="https://github.com/${REPO}/releases/download"

# Installation directory with fallback logic
INSTALL_DIR="${INSTALL_DIR:-}"
if [[ -z "$INSTALL_DIR" ]]; then
    if [[ -w "/usr/local/bin" ]] || command -v sudo &> /dev/null; then
        INSTALL_DIR="/usr/local/bin"
    else
        INSTALL_DIR="$HOME/bin"
        mkdir -p "$INSTALL_DIR"
    fi
fi

info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
    exit 1
}

# Detect platform and architecture
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Linux*)  os="linux" ;;
        Darwin*) os="darwin" ;;
        FreeBSD*) os="linux" ;; # Use Linux binary for FreeBSD
        CYGWIN*|MINGW*|MSYS*)
            error "Windows detected. Please use the PowerShell script: https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1"
            ;;
        *) error "Unsupported operating system: $(uname -s)" ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        armv6l|armv7l) arch="arm64" ;; # Use arm64 for ARM variants
        *) error "Unsupported architecture: $(uname -m)" ;;
    esac

    echo "${os}-${arch}"
}

# Get latest version from GitHub API
get_latest_version() {
    info "Fetching latest release information..."
    local version
    version=$(curl -fsSL "${GITHUB_API_URL}/releases/latest" | grep '"tag_name":' | cut -d'"' -f4)
    if [[ -z "$version" ]]; then
        error "Failed to fetch latest version from GitHub API"
    fi
    echo "$version"
}

# Download file with progress
download_file() {
    local url="$1"
    local output="$2"

    if command -v curl &> /dev/null; then
        curl -fsSL --progress-bar -o "$output" "$url"
    elif command -v wget &> /dev/null; then
        wget -q --show-progress -O "$output" "$url"
    else
        error "Neither curl nor wget found. Please install one of them."
    fi
}

# Verify checksum
verify_checksum() {
    local file="$1"
    local expected_file="$2"

    if [[ "${SKIP_CHECKSUM:-}" == "true" ]]; then
        warn "Skipping checksum verification (SKIP_CHECKSUM=true)"
        return 0
    fi

    info "Verifying checksum..."

    if command -v sha256sum &> /dev/null; then
        local actual
        actual=$(sha256sum "$file" | cut -d' ' -f1)
        local expected
        expected=$(grep "$(basename "$file")" "$expected_file" | cut -d' ' -f1)

        if [[ "$actual" != "$expected" ]]; then
            error "Checksum verification failed!\nExpected: $expected\nActual:   $actual"
        fi
        success "Checksum verified"
    elif command -v shasum &> /dev/null; then
        # macOS
        local actual
        actual=$(shasum -a 256 "$file" | cut -d' ' -f1)
        local expected
        expected=$(grep "$(basename "$file")" "$expected_file" | cut -d' ' -f1)

        if [[ "$actual" != "$expected" ]]; then
            error "Checksum verification failed!\nExpected: $expected\nActual:   $actual"
        fi
        success "Checksum verified"
    else
        warn "No checksum utility found (sha256sum or shasum). Skipping verification."
    fi
}

# Install binary
install_binary() {
    local binary_path="$1"
    local install_path="$INSTALL_DIR/$BINARY_NAME"

    info "Installing $BINARY_NAME to $install_path..."

    # Check if we need sudo
    if [[ ! -w "$INSTALL_DIR" ]]; then
        if command -v sudo &> /dev/null; then
            sudo mv "$binary_path" "$install_path"
            sudo chmod +x "$install_path"
        else
            error "Cannot write to $INSTALL_DIR and sudo not available"
        fi
    else
        mv "$binary_path" "$install_path"
        chmod +x "$install_path"
    fi

    success "$BINARY_NAME installed to $install_path"
}

# Update PATH if needed
update_path() {
    if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]] && [[ "$INSTALL_DIR" != "/usr/local/bin" ]]; then
        warn "$INSTALL_DIR is not in your PATH"
        echo ""
        echo "Add it to your PATH by adding this line to your shell profile:"
        echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
        echo ""
        echo "Or run commands with the full path: $INSTALL_DIR/$BINARY_NAME"
    fi
}

main() {
    echo "=== Alcove CLI Installer ==="
    echo ""

    # Detect platform
    local platform
    platform=$(detect_platform)
    info "Detected platform: $platform"

    # Get version
    local version="${ALCOVE_VERSION:-}"
    if [[ -z "$version" ]]; then
        version=$(get_latest_version)
    fi
    info "Installing version: $version"

    # Prepare download URLs
    local binary_name="${BINARY_NAME}-${platform}"
    if [[ "$platform" == "windows-"* ]]; then
        binary_name="${binary_name}.exe"
    fi

    local download_url="${DOWNLOAD_URL_PREFIX}/${version}/${binary_name}"
    local checksum_url="${DOWNLOAD_URL_PREFIX}/${version}/checksums-sha256.txt"

    # Create temporary directory
    local temp_dir
    temp_dir=$(mktemp -d)
    trap "rm -rf '$temp_dir'" EXIT

    local binary_file="$temp_dir/$binary_name"
    local checksum_file="$temp_dir/checksums-sha256.txt"

    # Download files
    info "Downloading $BINARY_NAME $version for $platform..."
    download_file "$download_url" "$binary_file"

    info "Downloading checksums..."
    download_file "$checksum_url" "$checksum_file"

    # Verify checksum
    verify_checksum "$binary_file" "$checksum_file"

    # Install
    install_binary "$binary_file"

    # Update PATH guidance
    update_path

    echo ""
    success "Installation complete!"
    echo ""
    echo "Verify your installation:"
    echo "  $BINARY_NAME version"
    echo ""
    echo "Get started:"
    echo "  $BINARY_NAME --help"
    echo "  $BINARY_NAME login https://your-bridge-instance.com"
    echo ""
}

main "$@"