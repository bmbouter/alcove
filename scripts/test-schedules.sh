#!/bin/bash
# test-schedules.sh — Tests for the schedule management API (YAML-only policy).
#
# Verifies that schedule mutation endpoints return 405 Method Not Allowed
# (schedules are now managed exclusively via YAML in agent repos) and that
# read-only listing still works.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-schedules.sh
#
# Tests:
#   - POST /api/v1/schedules returns 405
#   - PUT /api/v1/schedules/{id} returns 405
#   - POST /api/v1/schedules/{id}/enable returns 405
#   - DELETE /api/v1/schedules/{id} returns 405
#   - GET /api/v1/schedules still returns 200 (read-only listing)

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup ---
log "Setting up..."
ADMIN_LOGIN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}")
ADMIN_TOKEN=$(echo "$ADMIN_LOGIN" | python3 -c "import json,sys; d=json.load(sys.stdin); t=d.get('token',''); print(t) if t else sys.exit('Login failed: ' + json.dumps(d))")

# --- Test 1: POST /api/v1/schedules returns 405 ---
log "Test 1: POST /api/v1/schedules returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Should fail","cron":"0 * * * *","prompt":"test","provider":"vertex","timeout":60,"enabled":true}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "POST /api/v1/schedules returns 405 Method Not Allowed"
else
  fail "POST /api/v1/schedules returned $HTTP_CODE (expected 405)"
fi

# --- Test 2: PUT /api/v1/schedules/{id} returns 405 ---
log "Test 2: PUT /api/v1/schedules/{id} returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/schedules/fake-schedule-id" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Should fail","cron":"0 2 * * *","prompt":"updated prompt","provider":"vertex","timeout":120,"enabled":true}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "PUT /api/v1/schedules/{id} returns 405 Method Not Allowed"
else
  fail "PUT /api/v1/schedules/{id} returned $HTTP_CODE (expected 405)"
fi

# --- Test 3: POST /api/v1/schedules/{id}/enable returns 405 ---
log "Test 3: POST /api/v1/schedules/{id}/enable returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/schedules/fake-schedule-id/enable" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":false}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "POST /api/v1/schedules/{id}/enable returns 405 Method Not Allowed"
else
  fail "POST /api/v1/schedules/{id}/enable returned $HTTP_CODE (expected 405)"
fi

# --- Test 4: DELETE /api/v1/schedules/{id} returns 405 ---
log "Test 4: DELETE /api/v1/schedules/{id} returns 405"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/schedules/fake-schedule-id" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$HTTP_CODE" = "405" ]; then
  pass "DELETE /api/v1/schedules/{id} returns 405 Method Not Allowed"
else
  fail "DELETE /api/v1/schedules/{id} returned $HTTP_CODE (expected 405)"
fi

# --- Test 5: GET /api/v1/schedules still works ---
log "Test 5: GET /api/v1/schedules returns 200"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$HTTP_CODE" = "200" ]; then
  pass "GET /api/v1/schedules returns 200 (read-only listing works)"
else
  fail "GET /api/v1/schedules returned $HTTP_CODE (expected 200)"
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
