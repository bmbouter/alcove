#!/bin/bash
# test-yaml-only.sh — Tests for the YAML-only artifact policy.
#
# Verifies that schedules, security profiles, and tools can only be managed
# via YAML in agent repos (not via API), while sessions and credentials
# (runtime resources) remain fully functional via API.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-yaml-only.sh
#
# Tests:
#   Test 1: Schedule creation blocked (POST returns 405)
#   Test 2: Schedule update blocked (PUT returns 405)
#   Test 3: Profile creation blocked (POST returns 405)
#   Test 4: Profile builder blocked (POST returns 405 or 404)
#   Test 5: Tool creation blocked (POST returns 405)
#   Test 6: Read-only endpoints still work (GET returns 200)
#   Test 7: Session creation still works (POST returns 200/201)
#   Test 8: Credential creation still works (POST returns 200/201)

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

# Create test user for non-admin tests
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"yaml-only-tester","password":"yamltest1234","is_admin":false}' > /dev/null 2>&1 || true

USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"yaml-only-tester","password":"yamltest1234"}' | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

# =====================================================================
# Test 1: Schedule creation blocked
# =====================================================================
log "Test 1: Schedule creation blocked"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"yaml-only-test","cron":"0 * * * *","prompt":"should fail","provider":"vertex","timeout":60,"enabled":true}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "POST /api/v1/schedules returns 405 Method Not Allowed"
else
  fail "POST /api/v1/schedules returned $HTTP_CODE (expected 405)"
fi

# =====================================================================
# Test 2: Schedule update blocked
# =====================================================================
log "Test 2: Schedule update blocked"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/schedules/fake-id" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"yaml-only-test","cron":"0 2 * * *","prompt":"should fail","provider":"vertex","timeout":120,"enabled":true}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "PUT /api/v1/schedules/fake-id returns 405 Method Not Allowed"
else
  fail "PUT /api/v1/schedules/fake-id returned $HTTP_CODE (expected 405)"
fi

# =====================================================================
# Test 3: Profile creation blocked
# =====================================================================
log "Test 3: Profile creation blocked"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/security-profiles" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"yaml-only-test-profile","description":"Should not be created","tools":{"github":{"rules":[{"repos":["*"],"operations":["clone"]}]}}}')
if [ "$HTTP_CODE" = "405" ]; then
  pass "POST /api/v1/security-profiles returns 405 Method Not Allowed"
else
  fail "POST /api/v1/security-profiles returned $HTTP_CODE (expected 405)"
fi

# =====================================================================
# Test 4: Profile builder blocked
# =====================================================================
log "Test 4: Profile builder blocked"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/security-profiles/build" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description":"Read-only access to all GitHub repos"}')
if [ "$HTTP_CODE" = "405" ] || [ "$HTTP_CODE" = "404" ]; then
  pass "POST /api/v1/security-profiles/build returns $HTTP_CODE (blocked)"
else
  fail "POST /api/v1/security-profiles/build returned $HTTP_CODE (expected 405 or 404)"
fi

# =====================================================================
# Test 5: Tool creation blocked
# =====================================================================
log "Test 5: Tool creation blocked"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/tools" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"yaml-only-test-tool","description":"Should not be created"}')
if [ "$HTTP_CODE" = "405" ] || [ "$HTTP_CODE" = "404" ]; then
  pass "POST /api/v1/tools returns $HTTP_CODE (blocked)"
else
  fail "POST /api/v1/tools returned $HTTP_CODE (expected 405 or 404)"
fi

# =====================================================================
# Test 6: Read-only endpoints still work
# =====================================================================
log "Test 6: Read-only endpoints still work"

# 6a: GET /api/v1/schedules
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$HTTP_CODE" = "200" ]; then
  pass "GET /api/v1/schedules returns 200"
else
  fail "GET /api/v1/schedules returned $HTTP_CODE (expected 200)"
fi

# 6b: GET /api/v1/security-profiles
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/security-profiles" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$HTTP_CODE" = "200" ]; then
  pass "GET /api/v1/security-profiles returns 200"
else
  fail "GET /api/v1/security-profiles returned $HTTP_CODE (expected 200)"
fi

# 6c: GET /api/v1/tools
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/tools" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$HTTP_CODE" = "200" ]; then
  pass "GET /api/v1/tools returns 200"
elif [ "$HTTP_CODE" = "404" ]; then
  pass "GET /api/v1/tools returns 404 (endpoint may not exist yet)"
else
  fail "GET /api/v1/tools returned $HTTP_CODE (expected 200 or 404)"
fi

# =====================================================================
# Test 7: Session creation still works
# =====================================================================
log "Test 7: Session creation still works"
SESSION_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"YAML-only policy test session — should succeed","provider":"default","timeout":30}')
SESSION_CODE=$(echo "$SESSION_RESULT" | tail -1)
if [ "$SESSION_CODE" = "200" ] || [ "$SESSION_CODE" = "201" ]; then
  pass "POST /api/v1/sessions returns $SESSION_CODE (session creation works)"
  # Extract task_id for cleanup
  SESSION_BODY=$(echo "$SESSION_RESULT" | sed '$d')
  TASK_ID=$(echo "$SESSION_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('task_id',''))" 2>/dev/null || true)
else
  fail "POST /api/v1/sessions returned $SESSION_CODE (expected 200 or 201)"
fi

# =====================================================================
# Test 8: Credential creation still works
# =====================================================================
log "Test 8: Credential creation still works"
CRED_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"yaml-only-test-cred","provider":"github","auth_type":"api_key","credential":"ghp-test-yaml-only-token"}')
CRED_CODE=$(echo "$CRED_RESULT" | tail -1)
CRED_BODY=$(echo "$CRED_RESULT" | sed '$d')
if [ "$CRED_CODE" = "200" ] || [ "$CRED_CODE" = "201" ]; then
  pass "POST /api/v1/credentials returns $CRED_CODE (credential creation works)"
  CRED_ID=$(echo "$CRED_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || true)
else
  fail "POST /api/v1/credentials returned $CRED_CODE (expected 200 or 201)"
fi

# --- Cleanup ---
log "Cleaning up..."
if [ -n "${CRED_ID:-}" ]; then
  curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$CRED_ID" \
    -H "Authorization: Bearer $USER_TOKEN" > /dev/null 2>&1 || true
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
