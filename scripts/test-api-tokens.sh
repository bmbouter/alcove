#!/bin/bash
# test-api-tokens.sh — Tests for personal API token CRUD and authentication.
#
# Verifies creating, listing, authenticating with, and deleting personal API
# tokens. Also verifies that deleted tokens can no longer authenticate.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-api-tokens.sh
#
# Tests:
#   - Create API token
#   - List API tokens (verify name appears)
#   - Authenticate with API token
#   - Use JWT from token auth to access API
#   - Delete API token
#   - Deleted token no longer works

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
# Test 1: Create API token
# =====================================================================
log "Test 1: Create API token"
CREATE_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/auth/api-tokens" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"ci-test"}')
CREATE_HTTP=$(echo "$CREATE_RESULT" | tail -1)
CREATE_BODY=$(echo "$CREATE_RESULT" | sed '$d')
TOKEN_VALUE=$(echo "$CREATE_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")
TOKEN_ID=$(echo "$CREATE_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id',''))")

if [ "$CREATE_HTTP" = "201" ] && [ -n "$TOKEN_VALUE" ] && [ "$TOKEN_VALUE" != "" ]; then
  pass "Created API token (HTTP $CREATE_HTTP, token starts with ${TOKEN_VALUE:0:5}...)"
else
  fail "Failed to create API token (HTTP $CREATE_HTTP, body: $CREATE_BODY)"
fi

# =====================================================================
# Test 2: List API tokens (verify name appears)
# =====================================================================
log "Test 2: List API tokens"
LIST_RESULT=$(curl -s -w "\n%{http_code}" "$BRIDGE_URL/api/v1/auth/api-tokens" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
LIST_HTTP=$(echo "$LIST_RESULT" | tail -1)
LIST_BODY=$(echo "$LIST_RESULT" | sed '$d')
HAS_NAME=$(echo "$LIST_BODY" | python3 -c "import json,sys; tokens=json.load(sys.stdin); names=[t['name'] for t in tokens]; print('yes' if 'ci-test' in names else 'no')")

if [ "$LIST_HTTP" = "200" ] && [ "$HAS_NAME" = "yes" ]; then
  pass "Listed API tokens and found 'ci-test'"
else
  fail "Failed to find 'ci-test' in token list (HTTP $LIST_HTTP)"
fi

# =====================================================================
# Test 3: Authenticate with API token
# =====================================================================
log "Test 3: Authenticate with API token"
AUTH_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${TOKEN_VALUE}\"}")
AUTH_HTTP=$(echo "$AUTH_RESULT" | tail -1)
AUTH_BODY=$(echo "$AUTH_RESULT" | sed '$d')
TOKEN_JWT=$(echo "$AUTH_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

if [ "$AUTH_HTTP" = "200" ] && [ -n "$TOKEN_JWT" ] && [ "$TOKEN_JWT" != "" ]; then
  pass "Authenticated with API token (got JWT)"
else
  fail "Failed to authenticate with API token (HTTP $AUTH_HTTP, body: $AUTH_BODY)"
fi

# =====================================================================
# Test 4: Use JWT from token auth to access API
# =====================================================================
log "Test 4: Use JWT from token auth to access API"
SESSIONS_HTTP=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $TOKEN_JWT")

if [ "$SESSIONS_HTTP" = "200" ]; then
  pass "Accessed /api/v1/sessions with token-derived JWT (HTTP 200)"
else
  fail "Failed to access /api/v1/sessions with token-derived JWT (HTTP $SESSIONS_HTTP)"
fi

# =====================================================================
# Test 5: Delete API token
# =====================================================================
log "Test 5: Delete API token"
DEL_HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/auth/api-tokens/$TOKEN_ID" \
  -H "Authorization: Bearer $ADMIN_TOKEN")

if [ "$DEL_HTTP" = "200" ]; then
  pass "Deleted API token (HTTP 200)"
else
  fail "Failed to delete API token (HTTP $DEL_HTTP)"
fi

# =====================================================================
# Test 6: Deleted token no longer works
# =====================================================================
log "Test 6: Deleted token no longer works"
DEAD_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${TOKEN_VALUE}\"}")
DEAD_HTTP=$(echo "$DEAD_RESULT" | tail -1)

if [ "$DEAD_HTTP" = "401" ]; then
  pass "Deleted token correctly rejected (HTTP 401)"
else
  fail "Deleted token returned HTTP $DEAD_HTTP (expected 401)"
fi

# Summary
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
