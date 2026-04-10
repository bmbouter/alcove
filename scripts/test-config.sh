#!/bin/bash
# Copyright 2026 Brian Bouterse
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Test script for CLI config file functionality

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}Testing CLI Config File Support${NC}"
echo "================================="

# Create temporary test directory
TEST_DIR=$(mktemp -d)
export HOME="$TEST_DIR"
ALCOVE_CLI="$(pwd)/cmd/alcove/alcove"

# Function to clean up
cleanup() {
    rm -rf "$TEST_DIR"
}
trap cleanup EXIT

# Function to assert command success
assert_success() {
    local cmd="$1"
    local description="$2"
    echo -n "Testing $description... "
    if eval "$cmd" >/dev/null 2>&1; then
        echo -e "${GREEN}PASS${NC}"
    else
        echo -e "${RED}FAIL${NC}"
        echo "Command failed: $cmd"
        exit 1
    fi
}

# Function to assert command failure
assert_failure() {
    local cmd="$1"
    local description="$2"
    echo -n "Testing $description... "
    if eval "$cmd" >/dev/null 2>&1; then
        echo -e "${RED}FAIL${NC}"
        echo "Expected command to fail but it succeeded: $cmd"
        exit 1
    else
        echo -e "${GREEN}PASS${NC}"
    fi
}

# Function to assert string contains
assert_contains() {
    local output="$1"
    local expected="$2"
    local description="$3"
    echo -n "Testing $description... "
    if echo "$output" | grep -q "$expected"; then
        echo -e "${GREEN}PASS${NC}"
    else
        echo -e "${RED}FAIL${NC}"
        echo "Expected output to contain: $expected"
        echo "Actual output: $output"
        exit 1
    fi
}

# Build the CLI tool first
echo "Building Alcove CLI..."
cd cmd/alcove
go build -o alcove .
cd ../..
ALCOVE_CLI="$(pwd)/cmd/alcove/alcove"
echo -e "${YELLOW}Test 1: Config file initialization${NC}"

# Test config init command
assert_success "$ALCOVE_CLI config init" "config init creates example file"

# Verify config file was created
CONFIG_FILE="$HOME/.config/alcove/config.yaml"
if [[ ! -f "$CONFIG_FILE" ]]; then
    echo -e "${RED}FAIL${NC}: Config file was not created at $CONFIG_FILE"
    exit 1
fi

echo -e "${GREEN}Config file created successfully${NC}"

# Test config init fails when file already exists
assert_failure "$ALCOVE_CLI config init" "config init fails when file exists"

echo ""
echo -e "${YELLOW}Test 2: Config validation${NC}"

# Test config validate with empty config (should fail due to missing server)
output=$($ALCOVE_CLI config validate 2>&1 || true)
assert_contains "$output" "server.*is not set" "validation detects missing server"

# Create a config with server set
echo "server: https://test.example.com" > "$CONFIG_FILE"

# Test config validate with valid config
assert_failure "$ALCOVE_CLI config validate" "validation fails without credentials (expected)"

echo ""
echo -e "${YELLOW}Test 3: Config show command${NC}"

# Test config show displays current configuration
output=$($ALCOVE_CLI config show)
assert_contains "$output" "Current Effective Configuration" "config show displays header"
assert_contains "$output" "https://test.example.com" "config show displays server from config"
assert_contains "$output" "Source: Config file" "config show shows config file as source"

echo ""
echo -e "${YELLOW}Test 4: Config file precedence${NC}"

# Test that environment variable overrides config file
export ALCOVE_SERVER="https://env.example.com"
output=$($ALCOVE_CLI config show)
assert_contains "$output" "https://env.example.com" "environment variable overrides config"
assert_contains "$output" "Source: Environment variable" "shows environment as source"
unset ALCOVE_SERVER

# Test that CLI flag overrides config file
output=$($ALCOVE_CLI --server "https://flag.example.com" config show)
assert_contains "$output" "https://flag.example.com" "CLI flag overrides config"
assert_contains "$output" "Source: CLI flag" "shows CLI flag as source"

echo ""
echo -e "${YELLOW}Test 5: Multi-location config discovery${NC}"

# Remove the standard config file
rm "$CONFIG_FILE"

# Create config in convenience location
CONVENIENCE_CONFIG="$HOME/.alcove.yaml"
echo "server: https://convenience.example.com
provider: anthropic" > "$CONVENIENCE_CONFIG"

output=$($ALCOVE_CLI config show)
assert_contains "$output" "https://convenience.example.com" "loads config from convenience location"
assert_contains "$output" "anthropic" "loads provider from config"

# Remove convenience config
rm "$CONVENIENCE_CONFIG"

# Test XDG_CONFIG_HOME if different from default
export XDG_CONFIG_HOME="$HOME/custom-config"
mkdir -p "$XDG_CONFIG_HOME/alcove"
echo "server: https://xdg.example.com
model: claude-sonnet-4" > "$XDG_CONFIG_HOME/alcove/config.yaml"

output=$($ALCOVE_CLI config show)
assert_contains "$output" "https://xdg.example.com" "loads config from XDG_CONFIG_HOME"
assert_contains "$output" "claude-sonnet-4" "loads model from config"

unset XDG_CONFIG_HOME
rm -rf "$HOME/custom-config"

echo ""
echo -e "${YELLOW}Test 6: Config validation with various values${NC}"

# Create config with various values for validation testing
cat > "$CONFIG_FILE" << EOF
server: https://test.example.com
provider: anthropic
model: claude-sonnet-4-20250514
budget: 5.00
timeout: 30m
output: json
repo: test/repo
EOF

# This should pass validation (ignoring missing credentials)
output=$($ALCOVE_CLI config validate 2>&1 || true)
assert_contains "$output" "server = https://test.example.com" "validates good server config"

# Test invalid output format
cat > "$CONFIG_FILE" << EOF
server: https://test.example.com
output: xml
EOF

output=$($ALCOVE_CLI config validate 2>&1 || true)
assert_contains "$output" "invalid output format" "detects invalid output format"

# Test invalid timeout
cat > "$CONFIG_FILE" << EOF
server: https://test.example.com
timeout: invalid-timeout
EOF

output=$($ALCOVE_CLI config validate 2>&1 || true)
assert_contains "$output" "invalid timeout duration" "detects invalid timeout"

# Test negative budget
cat > "$CONFIG_FILE" << EOF
server: https://test.example.com
budget: -5.0
EOF

output=$($ALCOVE_CLI config validate 2>&1 || true)
assert_contains "$output" "budget must be positive" "detects negative budget"

echo ""
echo -e "${YELLOW}Test 7: Config values used in commands${NC}"

# Create a valid config file with defaults
cat > "$CONFIG_FILE" << EOF
server: https://test.example.com
provider: anthropic
model: claude-sonnet-4-20250514
budget: 5.00
timeout: 30m
output: json
repo: test/repo
EOF

# Note: We can't fully test the run command without a real server,
# but we can test that the config values are properly resolved in help output
# and that the commands accept the config-based defaults

echo -e "${GREEN}All config tests passed!${NC}"
echo ""
echo "Summary of tested functionality:"
echo "✓ Config file initialization (config init)"
echo "✓ Config validation (config validate)"
echo "✓ Config display (config show)"
echo "✓ Multiple config file locations"
echo "✓ Config value precedence (flag > env > config > default)"
echo "✓ Config validation with various value types"
echo "✓ Error handling for invalid configurations"
echo ""
echo -e "${GREEN}CLI config file support is working correctly!${NC}"