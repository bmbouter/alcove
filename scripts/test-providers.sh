#!/bin/bash
# test-providers.sh — Tests for the provider listing endpoint.
#
# Verifies that GET /api/v1/providers returns a valid JSON response
# with provider data when authenticated.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-providers.sh
#
# Tests:
#   - GET /api/v1/providers (authenticated) returns 200
#   - Response is valid JSON

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# Setup
log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

if [ -z "$ADMIN_TOKEN" ]; then
  echo "ERROR: Failed to obtain admin JWT. Is Bridge running and ADMIN_PASSWORD correct?"
  exit 1
fi

# =====================================================================
# Test 1: GET /api/v1/providers (authenticated) returns 200
# =====================================================================
log "Test 1: GET /api/v1/providers (authenticated)"
PROV_RESULT=$(curl -s -w "\n%{http_code}" "$BRIDGE_URL/api/v1/providers" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
PROV_HTTP=$(echo "$PROV_RESULT" | tail -1)
PROV_BODY=$(echo "$PROV_RESULT" | sed '$d')

if [ "$PROV_HTTP" = "200" ]; then
  pass "GET /api/v1/providers returns HTTP 200"
else
  fail "GET /api/v1/providers returned HTTP $PROV_HTTP (expected 200)"
fi

# =====================================================================
# Test 2: Response is valid JSON
# =====================================================================
log "Test 2: Response is valid JSON"
VALID_JSON=$(echo "$PROV_BODY" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    if isinstance(d, dict) and 'providers' in d:
        print('yes')
    else:
        print('no')
except:
    print('no')
")

if [ "$VALID_JSON" = "yes" ]; then
  PROVIDER_COUNT=$(echo "$PROV_BODY" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('providers',[])))")
  pass "Response is valid JSON with providers array ($PROVIDER_COUNT providers)"
else
  fail "Response is not valid JSON or missing providers key (body: $PROV_BODY)"
fi

# Summary
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
