#!/bin/bash
# test-system-state.sh — Tests for system pause/resume functionality.
#
# Verifies that admins can get and set the system mode (active/paused),
# that non-admin users are denied access, and that session creation is
# blocked while the system is paused.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-system-state.sh
#
# Tests:
#   - GET system state as admin
#   - GET system state as non-admin (expect 403)
#   - PUT system mode to paused
#   - POST session while paused (expect 503)
#   - PUT system mode to active
#   - POST session after resume (expect success)

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

cleanup() {
  # Ensure system is active on exit
  curl -s -X PUT "$BRIDGE_URL/api/v1/admin/system-state" \
    -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
    -d '{"mode":"active"}' > /dev/null 2>&1 || true
  # Delete test user
  curl -s -X DELETE "$BRIDGE_URL/api/v1/users/systest-user" \
    -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null 2>&1 || true
}
trap cleanup EXIT

# Setup
log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

if [ -z "$ADMIN_TOKEN" ]; then
  echo "ERROR: Failed to obtain admin JWT. Is Bridge running and ADMIN_PASSWORD correct?"
  exit 1
fi

# Create a non-admin test user
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"username":"systest-user","password":"systest123","is_admin":false}' > /dev/null 2>&1 || true

TEST_USER_ID=$(curl -s "$BRIDGE_URL/api/v1/users" -H "Authorization: Bearer $ADMIN_TOKEN" | \
  python3 -c "import json,sys; users=json.load(sys.stdin).get('users',[]); matches=[u['id'] for u in users if u['username']=='systest-user']; print(matches[0] if matches else '')" 2>/dev/null || true)

NONADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"systest-user","password":"systest123"}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

# =====================================================================
# Test 1: GET system state as admin
# =====================================================================
log "Test 1: GET system state as admin"
STATE_RESULT=$(curl -s -w "\n%{http_code}" "$BRIDGE_URL/api/v1/admin/system-state" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
STATE_HTTP=$(echo "$STATE_RESULT" | tail -1)
STATE_BODY=$(echo "$STATE_RESULT" | sed '$d')
HAS_MODE=$(echo "$STATE_BODY" | python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if 'mode' in d else 'no')")

if [ "$STATE_HTTP" = "200" ] && [ "$HAS_MODE" = "yes" ]; then
  pass "GET system state returns 200 with mode field"
else
  fail "GET system state returned HTTP $STATE_HTTP (expected 200 with mode field)"
fi

# =====================================================================
# Test 2: GET system state as non-admin (expect 403)
# =====================================================================
log "Test 2: GET system state as non-admin"
NONADMIN_HTTP=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/admin/system-state" \
  -H "Authorization: Bearer $NONADMIN_TOKEN")

if [ "$NONADMIN_HTTP" = "403" ]; then
  pass "Non-admin GET system state correctly returns 403"
else
  fail "Non-admin GET system state returned HTTP $NONADMIN_HTTP (expected 403)"
fi

# =====================================================================
# Test 3: PUT system mode to paused
# =====================================================================
log "Test 3: PUT system mode to paused"
PAUSE_RESULT=$(curl -s -w "\n%{http_code}" -X PUT "$BRIDGE_URL/api/v1/admin/system-state" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"mode":"paused"}')
PAUSE_HTTP=$(echo "$PAUSE_RESULT" | tail -1)
PAUSE_BODY=$(echo "$PAUSE_RESULT" | sed '$d')
PAUSE_MODE=$(echo "$PAUSE_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('mode',''))")

if [ "$PAUSE_HTTP" = "200" ] && [ "$PAUSE_MODE" = "paused" ]; then
  pass "System paused successfully (mode=paused)"
else
  fail "Failed to pause system (HTTP $PAUSE_HTTP, mode=$PAUSE_MODE)"
fi

# =====================================================================
# Test 4: POST session while paused (expect 503)
# =====================================================================
log "Test 4: POST session while paused"
SESSION_HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"prompt":"test session while paused"}')

if [ "$SESSION_HTTP" = "503" ]; then
  pass "Session creation correctly blocked while paused (HTTP 503)"
else
  fail "Session creation returned HTTP $SESSION_HTTP while paused (expected 503)"
fi

# =====================================================================
# Test 5: PUT system mode to active
# =====================================================================
log "Test 5: PUT system mode to active"
RESUME_RESULT=$(curl -s -w "\n%{http_code}" -X PUT "$BRIDGE_URL/api/v1/admin/system-state" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"mode":"active"}')
RESUME_HTTP=$(echo "$RESUME_RESULT" | tail -1)
RESUME_BODY=$(echo "$RESUME_RESULT" | sed '$d')
RESUME_MODE=$(echo "$RESUME_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('mode',''))")

if [ "$RESUME_HTTP" = "200" ] && [ "$RESUME_MODE" = "active" ]; then
  pass "System resumed successfully (mode=active)"
else
  fail "Failed to resume system (HTTP $RESUME_HTTP, mode=$RESUME_MODE)"
fi

# =====================================================================
# Test 6: POST session after resume (expect success)
# =====================================================================
log "Test 6: POST session after resume"
RESUME_SESSION_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"prompt":"test session after resume"}')
RESUME_SESSION_HTTP=$(echo "$RESUME_SESSION_RESULT" | tail -1)

# Accept 201 (created) or 500 (dispatch may fail without full infra, but not 503)
if [ "$RESUME_SESSION_HTTP" = "201" ] || [ "$RESUME_SESSION_HTTP" = "200" ]; then
  pass "Session creation accepted after resume (HTTP $RESUME_SESSION_HTTP)"
elif [ "$RESUME_SESSION_HTTP" = "503" ]; then
  fail "Session creation still blocked after resume (HTTP 503)"
else
  # 500 is acceptable — it means the request was accepted but dispatch failed
  # (e.g., no LLM credentials configured), which is different from 503 (paused)
  pass "Session creation not blocked after resume (HTTP $RESUME_SESSION_HTTP, dispatch may fail without full infra)"
fi

# Summary
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
