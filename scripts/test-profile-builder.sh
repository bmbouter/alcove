#!/bin/bash
# test-profile-builder.sh — Tests for the security profile builder API (YAML-only policy).
#
# Verifies that the profile builder and profile creation endpoints return
# 405 Method Not Allowed (security profiles are now managed exclusively
# via YAML in agent repos). GET endpoints still work.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-profile-builder.sh

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# Setup
log "Setting up..."
TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

# --- Test 1: POST /api/v1/security-profiles/build returns 405 ---
log "Test 1: POST /api/v1/security-profiles/build returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/security-profiles/build" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description":"Read-only access to pulp/pulpcore on GitHub"}')
if [ "$HTTP_CODE" = "405" ] || [ "$HTTP_CODE" = "404" ]; then
  pass "POST /api/v1/security-profiles/build returns $HTTP_CODE (blocked)"
else
  fail "POST /api/v1/security-profiles/build returned $HTTP_CODE (expected 405 or 404)"
fi

# --- Test 2: POST /api/v1/security-profiles returns 405 ---
log "Test 2: POST /api/v1/security-profiles returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/security-profiles" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"should-fail","description":"Should not be created","tools":{"github":{"rules":[{"repos":["*"],"operations":["clone"]}]}}}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "POST /api/v1/security-profiles returns 405 Method Not Allowed"
else
  fail "POST /api/v1/security-profiles returned $HTTP_CODE (expected 405)"
fi

# --- Test 3: PUT /api/v1/security-profiles/{name} returns 405 ---
log "Test 3: PUT /api/v1/security-profiles/{name} returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/security-profiles/nonexistent" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"nonexistent","description":"Should not work","tools":{}}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "PUT /api/v1/security-profiles/{name} returns 405 Method Not Allowed"
else
  fail "PUT /api/v1/security-profiles/{name} returned $HTTP_CODE (expected 405)"
fi

# --- Test 4: DELETE /api/v1/security-profiles/{name} returns 405 ---
log "Test 4: DELETE /api/v1/security-profiles/{name} returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/security-profiles/nonexistent" \
  -H "Authorization: Bearer $TOKEN")
if [ "$HTTP_CODE" = "405" ]; then
  pass "DELETE /api/v1/security-profiles/{name} returns 405 Method Not Allowed"
else
  fail "DELETE /api/v1/security-profiles/{name} returned $HTTP_CODE (expected 405)"
fi

# --- Test 5: GET /api/v1/security-profiles still works ---
log "Test 5: GET /api/v1/security-profiles returns 200"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/security-profiles" \
  -H "Authorization: Bearer $TOKEN")
if [ "$HTTP_CODE" = "200" ]; then
  pass "GET /api/v1/security-profiles returns 200 (read-only listing works)"
else
  fail "GET /api/v1/security-profiles returned $HTTP_CODE (expected 200)"
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
