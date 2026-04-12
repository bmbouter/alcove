#!/bin/bash
# test-user-isolation.sh — Multi-user isolation and authorization tests.
#
# Verifies that users can only access their own resources (credentials,
# sessions) and that admin-only endpoints are properly protected.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-user-isolation.sh
#
# Tests:
#   - Credential isolation (list, get-by-ID, delete across users)
#   - Session isolation (list, detail access across users)
#   - Admin-only endpoint authorization (user list)
#   - Pagination metadata on session listing

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup: Create test users ---
log "Setting up test environment..."

# Login as admin
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

# Create alice and bob (ignore errors if they exist)
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"iso-alice","password":"isoalice12","is_admin":false}' > /dev/null 2>&1 || true

curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"iso-bob","password":"isobob123","is_admin":false}' > /dev/null 2>&1 || true

# Login as alice and bob
ALICE_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"iso-alice","password":"isoalice12"}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

BOB_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"iso-bob","password":"isobob123"}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

echo "  Admin token: ${ADMIN_TOKEN:0:10}..."
echo "  Alice token: ${ALICE_TOKEN:0:10}..."
echo "  Bob token: ${BOB_TOKEN:0:10}..."

# --- Test: Credential isolation ---
log "Test: Credential isolation"

# Alice creates a credential
ALICE_CRED=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"alice-key","provider":"anthropic","auth_type":"api_key","credential":"sk-alice-test"}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
echo "  Alice credential: $ALICE_CRED"

# Bob creates a credential
BOB_CRED=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $BOB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"bob-key","provider":"anthropic","auth_type":"api_key","credential":"sk-bob-test"}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
echo "  Bob credential: $BOB_CRED"

# Alice lists credentials — should only see hers
ALICE_CREDS=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "import json,sys; d=json.load(sys.stdin); print(' '.join(c['name'] for c in d.get('credentials',[])))")

if echo "$ALICE_CREDS" | grep -q "alice-key"; then
  pass "Alice can see her own credential"
else
  fail "Alice cannot see her own credential"
fi

if echo "$ALICE_CREDS" | grep -q "bob-key"; then
  fail "Alice can see Bob's credential (should be denied)"
else
  pass "Alice cannot see Bob's credential"
fi

# Alice tries to access Bob's credential by ID
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/credentials/$BOB_CRED" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$HTTP_CODE" = "404" ] || [ "$HTTP_CODE" = "403" ]; then
  pass "Alice gets $HTTP_CODE on Bob's credential detail"
else
  fail "Alice gets $HTTP_CODE on Bob's credential detail (expected 403/404)"
fi

# Alice tries to delete Bob's credential
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
  "$BRIDGE_URL/api/v1/credentials/$BOB_CRED" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$HTTP_CODE" = "404" ] || [ "$HTTP_CODE" = "403" ]; then
  pass "Alice gets $HTTP_CODE deleting Bob's credential"
else
  fail "Alice gets $HTTP_CODE deleting Bob's credential (expected 403/404)"
fi

# --- Test: Session isolation ---
log "Test: Session isolation"

# Alice creates a session (may fail if no LLM provider configured)
ALICE_SESSION=$(curl -s -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Alice test","provider":"default","timeout":10}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
echo "  Alice session: $ALICE_SESSION"

# Bob creates a session
BOB_SESSION=$(curl -s -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $BOB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Bob test","provider":"default","timeout":10}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
echo "  Bob session: $BOB_SESSION"

if [ "$ALICE_SESSION" = "ERROR" ] || [ "$BOB_SESSION" = "ERROR" ]; then
  echo "  SKIP: Session tests skipped (no LLM provider configured)"
else
  # Alice lists sessions — should only see hers
  ALICE_SESSIONS=$(curl -s "$BRIDGE_URL/api/v1/sessions" \
    -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "import json,sys; d=json.load(sys.stdin); print(' '.join(s['id'] for s in d.get('sessions',[])))")

  if echo "$ALICE_SESSIONS" | grep -q "$ALICE_SESSION"; then
    pass "Alice can see her own session"
  else
    fail "Alice cannot see her own session"
  fi

  if echo "$ALICE_SESSIONS" | grep -q "$BOB_SESSION"; then
    fail "Alice can see Bob's session (should be denied)"
  else
    pass "Alice cannot see Bob's session"
  fi

  # Alice tries to access Bob's session detail
  HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    "$BRIDGE_URL/api/v1/sessions/$BOB_SESSION" \
    -H "Authorization: Bearer $ALICE_TOKEN")
  if [ "$HTTP_CODE" = "403" ]; then
    pass "Alice gets 403 on Bob's session detail"
  else
    fail "Alice gets $HTTP_CODE on Bob's session detail (expected 403)"
  fi
fi  # end session skip block

# --- Test: Admin authorization ---
log "Test: Admin authorization"

# Alice (non-admin) tries to list users
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$HTTP_CODE" = "403" ]; then
  pass "Non-admin Alice gets 403 on user list"
else
  fail "Non-admin Alice gets $HTTP_CODE on user list (expected 403)"
fi

# Admin can list users
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$HTTP_CODE" = "200" ]; then
  pass "Admin can list users"
else
  fail "Admin gets $HTTP_CODE on user list (expected 200)"
fi

# --- Test: Pagination ---
log "Test: Pagination"

PAGINATION=$(curl -s "$BRIDGE_URL/api/v1/sessions?page=1&per_page=2" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | python3 -c "
import json,sys
d = json.load(sys.stdin)
print(f\"page={d.get('page')} per_page={d.get('per_page')} total={d.get('total')} pages={d.get('pages')} count={d.get('count')}\")
")
echo "  Pagination response: $PAGINATION"

if echo "$PAGINATION" | grep -q "page=1"; then
  pass "Pagination returns page metadata"
else
  fail "Pagination missing page metadata"
fi

# --- Cleanup ---
log "Cleaning up test credentials..."
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$ALICE_CRED" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$BOB_CRED" \
  -H "Authorization: Bearer $BOB_TOKEN" > /dev/null 2>&1

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then
  exit 1
else
  echo "  All tests passed."
fi
