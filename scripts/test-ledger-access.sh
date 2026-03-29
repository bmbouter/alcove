#!/bin/bash
# test-ledger-access.sh — Session ownership and access control tests.
#
# Verifies that session data (detail, transcript, proxy log, cancel) is
# protected by ownership checks so users cannot access other users' sessions.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-ledger-access.sh
#
# Tests:
#   - Session list isolation between users
#   - Session detail ownership enforcement (403)
#   - Transcript access ownership enforcement
#   - Proxy log access ownership enforcement
#   - Cancel operation ownership enforcement

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS="${PASS:-0}"
FAIL="${FAIL:-0}"

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup ---
log "Setting up test users..."

# Login as admin (assumes bootstrap password is known)
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"'"$ADMIN_PASSWORD"'"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

# Create two test users
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"alice123"}' > /dev/null 2>&1 || true

curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"bob","password":"bob123"}' > /dev/null 2>&1 || true

# Login as alice and bob
ALICE_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"alice123"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

BOB_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"bob","password":"bob123"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

# --- Test 1: Create sessions as different users ---
log "Test 1: Creating sessions as alice and bob..."

ALICE_SESSION=$(curl -s -X POST "$BRIDGE_URL/api/v1/tasks" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Alice test prompt","provider":"default","timeout":10}' | python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")

BOB_SESSION=$(curl -s -X POST "$BRIDGE_URL/api/v1/tasks" \
  -H "Authorization: Bearer $BOB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Bob test prompt","provider":"default","timeout":10}' | python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")

echo "  Alice session: $ALICE_SESSION"
echo "  Bob session: $BOB_SESSION"

# --- Test 2: Alice can see her sessions but not Bob's ---
log "Test 2: Session list isolation..."

ALICE_SESSIONS=$(curl -s "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d = json.load(sys.stdin)
ids = [s['id'] for s in d.get('sessions',[])]
print(' '.join(ids))
")

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

# --- Test 3: Alice cannot access Bob's session detail ---
log "Test 3: Session detail ownership..."

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/sessions/$BOB_SESSION" \
  -H "Authorization: Bearer $ALICE_TOKEN")

if [ "$HTTP_CODE" = "403" ]; then
  pass "Alice gets 403 on Bob's session detail"
else
  fail "Alice gets $HTTP_CODE on Bob's session detail (expected 403)"
fi

# --- Test 4: Alice cannot read Bob's transcript ---
log "Test 4: Transcript ownership..."

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/sessions/$BOB_SESSION/transcript" \
  -H "Authorization: Bearer $ALICE_TOKEN")

if [ "$HTTP_CODE" = "403" ]; then
  pass "Alice gets 403 on Bob's transcript"
else
  fail "Alice gets $HTTP_CODE on Bob's transcript (expected 403)"
fi

# --- Test 5: Alice cannot read Bob's proxy log ---
log "Test 5: Proxy log ownership..."

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/sessions/$BOB_SESSION/proxy-log" \
  -H "Authorization: Bearer $ALICE_TOKEN")

if [ "$HTTP_CODE" = "403" ]; then
  pass "Alice gets 403 on Bob's proxy log"
else
  fail "Alice gets $HTTP_CODE on Bob's proxy log (expected 403)"
fi

# --- Test 6: Alice cannot cancel Bob's session ---
log "Test 6: Cancel ownership..."

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
  "$BRIDGE_URL/api/v1/sessions/$BOB_SESSION" \
  -H "Authorization: Bearer $ALICE_TOKEN")

if [ "$HTTP_CODE" = "403" ]; then
  pass "Alice gets 403 cancelling Bob's session"
else
  fail "Alice gets $HTTP_CODE cancelling Bob's session (expected 403)"
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo ""
  echo "  SOME TESTS FAILED"
  exit 1
else
  echo ""
  echo "  All tests passed."
fi
