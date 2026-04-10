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

# Functional test script for config file support

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ALCOVE_BIN="$PROJECT_DIR/alcove"

# Build alcove if it doesn't exist or source is newer
if [[ ! -f "$ALCOVE_BIN" ]] || [[ "$PROJECT_DIR/cmd/alcove/main.go" -nt "$ALCOVE_BIN" ]]; then
    echo "Building alcove CLI..."
    cd "$PROJECT_DIR"
    go build -o alcove ./cmd/alcove
fi

# Test working directory
TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT

echo "Running config functionality tests in $TEST_DIR"

# Test 1: Config init command
echo "Test 1: Config init command"
HOME="$TEST_DIR/test1" "$ALCOVE_BIN" config init >/dev/null
if [[ ! -f "$TEST_DIR/test1/.config/alcove/config.yaml" ]]; then
    echo "FAIL: Config init did not create config file"
    exit 1
fi

# Verify config file has expected content
if ! grep -q "# Alcove CLI Configuration" "$TEST_DIR/test1/.config/alcove/config.yaml"; then
    echo "FAIL: Config init did not create properly formatted file"
    exit 1
fi
echo "PASS: Config init works correctly"

# Test 2: Config init refuses to overwrite
echo "Test 2: Config init refuses to overwrite existing config"
if HOME="$TEST_DIR/test1" "$ALCOVE_BIN" config init 2>/dev/null; then
    echo "FAIL: Config init should refuse to overwrite existing config"
    exit 1
fi
echo "PASS: Config init properly refuses to overwrite"

# Test 3: Config file loading and precedence
echo "Test 3: Config file loading and precedence"
mkdir -p "$TEST_DIR/test3/.config/alcove"
cat > "$TEST_DIR/test3/.config/alcove/config.yaml" << 'EOF'
server: https://xdg.example.com
provider: xdg-provider
model: xdg-model
budget: 10.0
timeout: 30m
output: json
repo: xdg/repo
EOF

# Test config show in JSON format
CONFIG_OUTPUT=$(HOME="$TEST_DIR/test3" "$ALCOVE_BIN" config show --output json)
if ! echo "$CONFIG_OUTPUT" | jq -e '.server == "https://xdg.example.com"' >/dev/null; then
    echo "FAIL: Config show did not load XDG config correctly"
    exit 1
fi

if ! echo "$CONFIG_OUTPUT" | jq -e '.provider == "xdg-provider"' >/dev/null; then
    echo "FAIL: Config show did not load provider from config"
    exit 1
fi

if ! echo "$CONFIG_OUTPUT" | jq -e '.budget == 10' >/dev/null; then
    echo "FAIL: Config show did not load budget from config"
    exit 1
fi
echo "PASS: Config loading works correctly"

# Test 4: Alternative config location (~/.alcove.yaml)
echo "Test 4: Alternative config location"
mkdir -p "$TEST_DIR/test4"
cat > "$TEST_DIR/test4/.alcove.yaml" << 'EOF'
server: https://convenience.example.com
provider: convenience-provider
output: table
EOF

CONFIG_OUTPUT=$(HOME="$TEST_DIR/test4" "$ALCOVE_BIN" config show --output json)
if ! echo "$CONFIG_OUTPUT" | jq -e '.server == "https://convenience.example.com"' >/dev/null; then
    echo "FAIL: Alternative config location not working"
    exit 1
fi

if ! echo "$CONFIG_OUTPUT" | jq -e '.provider == "convenience-provider"' >/dev/null; then
    echo "FAIL: Alternative config location not loading provider"
    exit 1
fi
echo "PASS: Alternative config location works"

# Test 5: Config priority (XDG over convenience)
echo "Test 5: Config file priority"
mkdir -p "$TEST_DIR/test5/.config/alcove"
cat > "$TEST_DIR/test5/.config/alcove/config.yaml" << 'EOF'
server: https://priority-xdg.example.com
provider: priority-xdg
EOF

cat > "$TEST_DIR/test5/.alcove.yaml" << 'EOF'
server: https://priority-convenience.example.com
provider: priority-convenience
EOF

CONFIG_OUTPUT=$(HOME="$TEST_DIR/test5" "$ALCOVE_BIN" config show --output json)
if ! echo "$CONFIG_OUTPUT" | jq -e '.server == "https://priority-xdg.example.com"' >/dev/null; then
    echo "FAIL: XDG config should have priority over convenience location"
    exit 1
fi
echo "PASS: Config file priority works correctly"

# Test 6: Flag precedence over config file
echo "Test 6: Flag precedence over config"
CONFIG_OUTPUT=$(HOME="$TEST_DIR/test3" "$ALCOVE_BIN" config show --output table)
# With --output table flag, the output should be in table format, not JSON
if echo "$CONFIG_OUTPUT" | grep -q '{'; then
    echo "FAIL: Flag should override config file setting"
    exit 1
fi
if ! echo "$CONFIG_OUTPUT" | grep -q "Current effective configuration"; then
    echo "FAIL: Table output not working correctly"
    exit 1
fi
echo "PASS: Flag precedence works correctly"

# Test 7: Config validation
echo "Test 7: Config validation"
mkdir -p "$TEST_DIR/test7/.config/alcove"
cat > "$TEST_DIR/test7/.config/alcove/config.yaml" << 'EOF'
server: https://valid.example.com
provider: valid-provider
model: valid-model
budget: 5.0
timeout: 45m
output: json
repo: valid/repo
EOF

# Without credentials file, validation should report missing credentials
VALIDATION_OUTPUT=$(HOME="$TEST_DIR/test7" "$ALCOVE_BIN" config validate 2>&1 || true)

if ! echo "$VALIDATION_OUTPUT" | grep -q "found configuration with 7 fields"; then
    echo "FAIL: Validation should report number of config fields"
    echo "Actual output: $VALIDATION_OUTPUT"
    exit 1
fi

if ! echo "$VALIDATION_OUTPUT" | grep -q "cannot read.*credentials"; then
    echo "FAIL: Validation should report missing credentials"
    echo "Actual output: $VALIDATION_OUTPUT"
    exit 1
fi
echo "PASS: Config validation works correctly"

# Test 8: Invalid config validation
echo "Test 8: Invalid config validation"
mkdir -p "$TEST_DIR/test8/.config/alcove"
cat > "$TEST_DIR/test8/.config/alcove/config.yaml" << 'EOF'
server: https://invalid.example.com
output: invalid-format
timeout: invalid-duration
budget: -5.0
EOF

VALIDATION_OUTPUT=$(HOME="$TEST_DIR/test8" "$ALCOVE_BIN" config validate 2>&1 || true)
if ! echo "$VALIDATION_OUTPUT" | grep -q "invalid output format"; then
    echo "FAIL: Should detect invalid output format"
    exit 1
fi

if ! echo "$VALIDATION_OUTPUT" | grep -q "invalid timeout format"; then
    echo "FAIL: Should detect invalid timeout format"
    exit 1
fi

if ! echo "$VALIDATION_OUTPUT" | grep -q "budget cannot be negative"; then
    echo "FAIL: Should detect negative budget"
    exit 1
fi
echo "PASS: Invalid config validation works correctly"

# Test 9: Backward compatibility with server-only config
echo "Test 9: Backward compatibility"
mkdir -p "$TEST_DIR/test9/.config/alcove"
cat > "$TEST_DIR/test9/.config/alcove/config.yaml" << 'EOF'
server: https://legacy.example.com
EOF

CONFIG_OUTPUT=$(HOME="$TEST_DIR/test9" "$ALCOVE_BIN" config show --output json)
if ! echo "$CONFIG_OUTPUT" | jq -e '.server == "https://legacy.example.com"' >/dev/null; then
    echo "FAIL: Backward compatibility broken for server-only config"
    exit 1
fi

# Verify other fields are empty/default
if ! echo "$CONFIG_OUTPUT" | jq -e '.provider == ""' >/dev/null; then
    echo "FAIL: Provider should be empty in server-only config"
    exit 1
fi

if ! echo "$CONFIG_OUTPUT" | jq -e '.budget == 0' >/dev/null; then
    echo "FAIL: Budget should be 0 in server-only config"
    exit 1
fi
echo "PASS: Backward compatibility works correctly"

# Test 10: Environment variable precedence
echo "Test 10: Environment variable precedence"
CONFIG_OUTPUT=$(ALCOVE_SERVER="https://env-override.example.com" HOME="$TEST_DIR/test3" "$ALCOVE_BIN" config show --output json)
if ! echo "$CONFIG_OUTPUT" | jq -e '.server == "https://env-override.example.com"' >/dev/null; then
    echo "FAIL: Environment variable should override config file"
    echo "Actual output: $CONFIG_OUTPUT"
    exit 1
fi
echo "PASS: Environment variable precedence works correctly"

echo ""
echo "All config functionality tests PASSED!"
echo "✅ Config init command"
echo "✅ Config overwrite protection"
echo "✅ Config file loading"
echo "✅ Alternative config location (~/.alcove.yaml)"
echo "✅ Config file priority (XDG over convenience)"
echo "✅ Flag precedence over config"
echo "✅ Config validation"
echo "✅ Invalid config detection"
echo "✅ Backward compatibility"
echo "✅ Environment variable precedence"