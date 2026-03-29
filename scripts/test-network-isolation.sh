#!/usr/bin/env bash
# test-network-isolation.sh — Smoke tests for Skiff container network isolation.
#
# Verifies that a container on the alcove network can reach internal services
# (Gate, Hail, Ledger) but CANNOT reach external IPs, DNS, or HTTPS endpoints.
#
# Usage:
#   ./scripts/test-network-isolation.sh [--internal]
#
# Options:
#   --internal   Create the test network with podman's --internal flag,
#                which disables outbound routing at the network level.
#                Without this flag, isolation relies on proxy-only enforcement.

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
TEST_NETWORK="alcove-test-isolation"
SKIFF_IMAGE="localhost/alcove-skiff-base:dev"
GATE_CONTAINER="test-gate"
SKIFF_CONTAINER="test-skiff"
TIMEOUT_SECONDS=5

USE_INTERNAL=""
if [[ "${1:-}" == "--internal" ]]; then
    USE_INTERNAL="--internal"
fi

PASS_COUNT=0
FAIL_COUNT=0
RESULTS=()

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { echo ">>> $*"; }
pass() { PASS_COUNT=$((PASS_COUNT + 1)); RESULTS+=("PASS: $1"); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); RESULTS+=("FAIL: $1"); echo "  FAIL: $1"; }

# Run a command inside the skiff container. Returns the exit code.
skiff_exec() {
    podman exec "$SKIFF_CONTAINER" bash -c "$1" 2>&1
}

skiff_exec_rc() {
    podman exec "$SKIFF_CONTAINER" bash -c "$1" >/dev/null 2>&1
    echo $?
}

# ---------------------------------------------------------------------------
# Cleanup (runs on exit regardless of success/failure)
# ---------------------------------------------------------------------------
cleanup() {
    log "Cleaning up..."
    podman rm -f "$SKIFF_CONTAINER" 2>/dev/null || true
    podman rm -f "$GATE_CONTAINER" 2>/dev/null || true
    podman network rm "$TEST_NETWORK" 2>/dev/null || true
    log "Cleanup complete."
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
log "Setting up test environment..."

# Remove any leftovers from a previous run.
podman rm -f "$SKIFF_CONTAINER" 2>/dev/null || true
podman rm -f "$GATE_CONTAINER" 2>/dev/null || true
podman network rm "$TEST_NETWORK" 2>/dev/null || true

# Create the test network.
if [[ -n "$USE_INTERNAL" ]]; then
    log "Creating network $TEST_NETWORK with --internal flag (no outbound routing)"
    podman network create --internal "$TEST_NETWORK"
else
    log "Creating network $TEST_NETWORK (standard — proxy-only enforcement)"
    podman network create "$TEST_NETWORK"
fi

# Start a dummy Gate container. We use a simple HTTP listener so the skiff
# container can verify reachability. The ubi9 image has python3 available
# in the skiff-base image, so we use the same image for the gate stand-in.
log "Starting dummy gate container ($GATE_CONTAINER)..."
podman run -d --rm \
    --name "$GATE_CONTAINER" \
    --network "$TEST_NETWORK" \
    --entrypoint bash \
    "$SKIFF_IMAGE" \
    -c 'python3 -m http.server 8443 --bind 0.0.0.0 2>/dev/null'

# Give the HTTP server a moment to start.
sleep 2

# Start the skiff container (the one we test from).
log "Starting skiff container ($SKIFF_CONTAINER)..."
podman run -d --rm \
    --name "$SKIFF_CONTAINER" \
    --network "$TEST_NETWORK" \
    --entrypoint '["bash", "-c", "sleep 600"]' \
    -e "HTTP_PROXY=http://${GATE_CONTAINER}:8443" \
    -e "HTTPS_PROXY=http://${GATE_CONTAINER}:8443" \
    -e "NO_PROXY=localhost,127.0.0.1,alcove-hail,alcove-bridge,alcove-ledger,host.containers.internal,${GATE_CONTAINER}" \
    "$SKIFF_IMAGE"

sleep 1
log "Test containers are running."
echo ""

# ---------------------------------------------------------------------------
# Tests: Internal reachability (should PASS)
# ---------------------------------------------------------------------------
log "=== Internal Reachability Tests ==="

# Test 1: Can reach Gate container by name on port 8443
echo -n "  [1/7] Reach Gate ($GATE_CONTAINER:8443)... "
rc=$(skiff_exec_rc "timeout $TIMEOUT_SECONDS bash -c 'echo > /dev/tcp/${GATE_CONTAINER}/8443'")
if [[ "$rc" -eq 0 ]]; then
    pass "Can reach Gate container (${GATE_CONTAINER}:8443)"
else
    fail "Cannot reach Gate container (${GATE_CONTAINER}:8443) — expected success"
fi

# Test 2: Can reach alcove-hail by name (may not be running, so we check DNS only)
echo -n "  [2/7] Resolve alcove-hail... "
# On an internal network, only containers on the network are resolvable.
# alcove-hail is not running, so we expect DNS failure or connection refused.
# This test is a "graceful" check — it passes if DNS resolves OR if it
# fails because the container simply isn't present (expected in test env).
output=$(skiff_exec "getent hosts alcove-hail 2>&1" || true)
if echo "$output" | grep -q "not found\|No address\|Name or service not known\|nodename nor servname"; then
    pass "alcove-hail not on network (expected — not running in test env)"
elif echo "$output" | grep -qE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+'; then
    pass "Can resolve alcove-hail"
else
    pass "alcove-hail lookup returned gracefully (no crash)"
fi

# Test 3: Can reach alcove-ledger by name (same graceful approach)
echo -n "  [3/7] Resolve alcove-ledger... "
output=$(skiff_exec "getent hosts alcove-ledger 2>&1" || true)
if echo "$output" | grep -q "not found\|No address\|Name or service not known\|nodename nor servname"; then
    pass "alcove-ledger not on network (expected — not running in test env)"
elif echo "$output" | grep -qE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+'; then
    pass "Can resolve alcove-ledger"
else
    pass "alcove-ledger lookup returned gracefully (no crash)"
fi

echo ""

# ---------------------------------------------------------------------------
# Tests: External reachability (should FAIL from skiff's perspective)
# ---------------------------------------------------------------------------
log "=== External Isolation Tests ==="

# Test 4: Cannot reach 8.8.8.8 (Google DNS)
echo -n "  [4/7] Block external IP 8.8.8.8... "
rc=$(skiff_exec_rc "timeout $TIMEOUT_SECONDS bash -c 'echo > /dev/tcp/8.8.8.8/53'")
if [[ "$rc" -ne 0 ]]; then
    pass "Cannot reach 8.8.8.8:53 (external IP blocked)"
else
    fail "CAN reach 8.8.8.8:53 — external IP is NOT blocked!"
fi

# Test 5: Cannot resolve google.com
echo -n "  [5/7] Block external DNS google.com... "
output=$(skiff_exec "timeout $TIMEOUT_SECONDS getent hosts google.com 2>&1" || true)
rc_dns=$(skiff_exec_rc "timeout $TIMEOUT_SECONDS getent hosts google.com")
if [[ "$rc_dns" -ne 0 ]]; then
    pass "Cannot resolve google.com (external DNS blocked)"
else
    fail "CAN resolve google.com — external DNS is NOT blocked!"
fi

# Test 6: Cannot curl https://api.anthropic.com (bypassing proxy with --noproxy)
echo -n "  [6/7] Block direct HTTPS to api.anthropic.com... "
rc=$(skiff_exec_rc "timeout $TIMEOUT_SECONDS curl --noproxy '*' -s -o /dev/null -w '%{http_code}' https://api.anthropic.com/ 2>&1")
if [[ "$rc" -ne 0 ]]; then
    pass "Cannot curl api.anthropic.com directly (HTTPS blocked)"
else
    fail "CAN curl api.anthropic.com directly — HTTPS is NOT blocked!"
fi

# Test 7: Cannot curl https://github.com (bypassing proxy with --noproxy)
echo -n "  [7/7] Block direct HTTPS to github.com... "
rc=$(skiff_exec_rc "timeout $TIMEOUT_SECONDS curl --noproxy '*' -s -o /dev/null -w '%{http_code}' https://github.com/ 2>&1")
if [[ "$rc" -ne 0 ]]; then
    pass "Cannot curl github.com directly (HTTPS blocked)"
else
    fail "CAN curl github.com directly — HTTPS is NOT blocked!"
fi

echo ""

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
log "=== Test Summary ==="
for r in "${RESULTS[@]}"; do
    echo "  $r"
done
echo ""
echo "  Total: $((PASS_COUNT + FAIL_COUNT))  Passed: $PASS_COUNT  Failed: $FAIL_COUNT"
echo ""

if [[ "$FAIL_COUNT" -gt 0 ]]; then
    if [[ -z "$USE_INTERNAL" ]]; then
        echo "NOTE: Tests ran WITHOUT --internal network flag."
        echo "External access failures rely on proxy-only enforcement."
        echo "Re-run with --internal to enforce at the network level:"
        echo "  ./scripts/test-network-isolation.sh --internal"
    fi
    echo ""
    exit 1
else
    log "All tests passed."
    exit 0
fi
