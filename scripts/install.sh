#!/bin/bash
# Alcove CLI Installation Script
# 
# This script installs the Alcove CLI by downloading the appropriate binary
# for your platform from the latest GitHub release.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
#   
# Or with custom installation directory:
#   curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | INSTALL_DIR="$HOME/bin" bash

set -e

# Configuration
REPO="bmbouter/alcove"
BINARY_NAME="alcove"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
GITHUB_URL="https://api.github.com/repos/${REPO}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Print functions
info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
    exit 1
}

# Detect platform and architecture
detect_platform() {
    local os arch
    
    case "$(uname -s)" in
        Linux*)     os="linux" ;;
        Darwin*)    os="darwin" ;;
        FreeBSD*)   os="linux" ;;  # Use linux binary for FreeBSD
        *)          error "Unsupported operating system: $(uname -s)" ;;
    esac
    
    case "$(uname -m)" in
        x86_64|amd64)   arch="amd64" ;;
        arm64|aarch64)  arch="arm64" ;;
        *)              error "Unsupported architecture: $(uname -m)" ;;
    esac
    
    echo "${os}-${arch}"
}

# Get latest release info
get_latest_release() {
    info "Fetching latest release information..."
    
    if command -v curl >/dev/null 2>&1; then
        curl -s "${GITHUB_URL}/releases/latest"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "${GITHUB_URL}/releases/latest"
    else
        error "Neither curl nor wget found. Please install one of them."
    fi
}

# Download and verify binary
download_binary() {
    local platform="$1"
    local version="$2"
    local download_url="$3"
    local checksums_url="$4"
    local temp_dir
    
    temp_dir=$(mktemp -d)
    local binary_file="${temp_dir}/${BINARY_NAME}-${platform}"
    local checksums_file="${temp_dir}/checksums-sha256.txt"
    
    info "Downloading ${BINARY_NAME} ${version} for ${platform}..."
    
    # Download binary
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${binary_file}" "${download_url}" || error "Failed to download binary"
        curl -fsSL -o "${checksums_file}" "${checksums_url}" || warning "Failed to download checksums (skipping verification)"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "${binary_file}" "${download_url}" || error "Failed to download binary"
        wget -q -O "${checksums_file}" "${checksums_url}" || warning "Failed to download checksums (skipping verification)"
    fi
    
    # Verify checksum if available
    if [ -f "${checksums_file}" ] && command -v sha256sum >/dev/null 2>&1; then
        info "Verifying checksum..."
        local expected_checksum
        expected_checksum=$(grep "$(basename "${binary_file}")" "${checksums_file}" | cut -d' ' -f1)
        if [ -n "${expected_checksum}" ]; then
            local actual_checksum
            actual_checksum=$(sha256sum "${binary_file}" | cut -d' ' -f1)
            if [ "${actual_checksum}" = "${expected_checksum}" ]; then
                success "Checksum verified successfully"
            else
                error "Checksum verification failed! Expected: ${expected_checksum}, Got: ${actual_checksum}"
            fi
        else
            warning "Checksum not found for ${platform} platform"
        fi
    else
        warning "Skipping checksum verification (sha256sum not available or checksums not downloaded)"
    fi
    
    echo "${binary_file}"
}

# Install binary
install_binary() {
    local binary_file="$1"
    local install_path="${INSTALL_DIR}/${BINARY_NAME}"
    
    # Create install directory if it doesn't exist
    if [ ! -d "${INSTALL_DIR}" ]; then
        info "Creating installation directory: ${INSTALL_DIR}"
        if ! mkdir -p "${INSTALL_DIR}" 2>/dev/null; then
            error "Failed to create ${INSTALL_DIR}. Try running with sudo or setting INSTALL_DIR to a writable directory."
        fi
    fi
    
    # Check if we can write to the install directory
    if [ ! -w "${INSTALL_DIR}" ]; then
        error "No write permission for ${INSTALL_DIR}. Try running with sudo or setting INSTALL_DIR to a writable directory."
    fi
    
    info "Installing ${BINARY_NAME} to ${install_path}..."
    
    # Make binary executable and copy
    chmod +x "${binary_file}"
    cp "${binary_file}" "${install_path}" || error "Failed to install binary"
    
    success "${BINARY_NAME} installed successfully to ${install_path}"
    
    # Check if install directory is in PATH
    if ! echo ":${PATH}:" | grep -q ":${INSTALL_DIR}:"; then
        warning "${INSTALL_DIR} is not in your PATH. Add it to your shell profile:"
        echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
        echo ""
        echo "Or run with full path: ${install_path}"
    fi
}

# Test installation
test_installation() {
    local install_path="${INSTALL_DIR}/${BINARY_NAME}"
    
    info "Testing installation..."
    
    if "${install_path}" version >/dev/null 2>&1; then
        local version_output
        version_output=$("${install_path}" version 2>/dev/null)
        success "Installation test passed: ${version_output}"
    else
        warning "Installation test failed. The binary may not be working correctly."
        echo "Try running: ${install_path} --help"
    fi
}

# Main installation flow
main() {
    echo ""
    echo "🚀 Alcove CLI Installer"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Detect platform
    local platform
    platform=$(detect_platform)
    info "Detected platform: ${platform}"
    
    # Get release information
    local release_json
    release_json=$(get_latest_release)
    
    local version
    version=$(echo "${release_json}" | grep '"tag_name":' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/')
    
    if [ -z "${version}" ]; then
        error "Failed to parse latest version from GitHub API"
    fi
    
    info "Latest version: ${version}"
    
    # Construct download URLs
    local binary_filename="${BINARY_NAME}-${platform}"
    local download_url="https://github.com/${REPO}/releases/download/${version}/${binary_filename}"
    local checksums_url="https://github.com/${REPO}/releases/download/${version}/checksums-sha256.txt"
    
    # Download and verify
    local binary_file
    binary_file=$(download_binary "${platform}" "${version}" "${download_url}" "${checksums_url}")
    
    # Install
    install_binary "${binary_file}"
    
    # Test
    test_installation
    
    echo ""
    success "Alcove CLI installation complete!"
    echo ""
    echo "Next steps:"
    echo "  1. Connect to your Bridge instance:"
    echo "     ${BINARY_NAME} login https://your-bridge-instance.com"
    echo ""
    echo "  2. Submit a task:"
    echo "     ${BINARY_NAME} run \"Fix the bug in the login function\""
    echo ""
    echo "  3. Get help:"
    echo "     ${BINARY_NAME} --help"
    echo ""
    
    # Cleanup
    rm -rf "$(dirname "${binary_file}")"
}

# Run installer
main "$@"
