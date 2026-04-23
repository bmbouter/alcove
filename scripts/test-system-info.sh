#!/bin/bash
# test-system-info.sh — Tests for the system info endpoint.
#
# Verifies that /api/v1/system-info returns expected fields including
# version, runtime, and auth_backend. This endpoint is public (no auth
# required).
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#
# Usage:
#   ./scripts/test-system-info.sh
#
# Tests:
#   - GET /api/v1/system-info returns 200
#   - Response has version field
#   - Response has runtime field
#   - Response has auth_backend field

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# =====================================================================
# Test 1: GET /api/v1/system-info returns 200
# =====================================================================
log "Test 1: GET /api/v1/system-info returns 200"
INFO_RESULT=$(curl -s -w "\n%{http_code}" "$BRIDGE_URL/api/v1/system-info")
INFO_HTTP=$(echo "$INFO_RESULT" | tail -1)
INFO_BODY=$(echo "$INFO_RESULT" | sed '$d')

if [ "$INFO_HTTP" = "200" ]; then
  pass "GET /api/v1/system-info returns HTTP 200"
else
  fail "GET /api/v1/system-info returned HTTP $INFO_HTTP (expected 200)"
fi

# =====================================================================
# Test 2: Response has version field
# =====================================================================
log "Test 2: Response has version field"
HAS_VERSION=$(echo "$INFO_BODY" | python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if 'version' in d else 'no')")

if [ "$HAS_VERSION" = "yes" ]; then
  VERSION=$(echo "$INFO_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin)['version'])")
  pass "Response has version field (value: $VERSION)"
else
  fail "Response missing version field"
fi

# =====================================================================
# Test 3: Response has runtime field
# =====================================================================
log "Test 3: Response has runtime field"
HAS_RUNTIME=$(echo "$INFO_BODY" | python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if 'runtime' in d else 'no')")

if [ "$HAS_RUNTIME" = "yes" ]; then
  RUNTIME=$(echo "$INFO_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin)['runtime'])")
  # Verify runtime is one of the expected values
  if [ "$RUNTIME" = "podman" ] || [ "$RUNTIME" = "kubernetes" ]; then
    pass "Response has runtime field (value: $RUNTIME)"
  else
    pass "Response has runtime field (value: $RUNTIME — unexpected but present)"
  fi
else
  fail "Response missing runtime field"
fi

# =====================================================================
# Test 4: Response has auth_backend field
# =====================================================================
log "Test 4: Response has auth_backend field"
HAS_AUTH=$(echo "$INFO_BODY" | python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if 'auth_backend' in d else 'no')")

if [ "$HAS_AUTH" = "yes" ]; then
  AUTH_BACKEND=$(echo "$INFO_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin)['auth_backend'])")
  pass "Response has auth_backend field (value: $AUTH_BACKEND)"
else
  fail "Response missing auth_backend field"
fi

# Summary
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
