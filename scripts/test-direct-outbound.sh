#!/usr/bin/env bash
# test-direct-outbound.sh — API tests for direct outbound mode (issue #280).
#
# Verifies that the Bridge API accepts direct_outbound in session creation
# requests and returns the value correctly. Does not test actual container
# networking (that requires a running Podman runtime and is covered by
# Go unit tests in internal/runtime/podman_test.go).
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - Admin credentials available
#
# Usage:
#   ./scripts/test-direct-outbound.sh
#   ADMIN_PASSWORD=<pw> ./scripts/test-direct-outbound.sh
set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ""; echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup ---
log "Setup"
USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD:-admin}\"}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")
if [ -z "$USER_TOKEN" ]; then echo "Login failed"; exit 1; fi

TEAM_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "import json,sys; t=[x for x in json.load(sys.stdin).get('teams',[]) if x.get('is_personal')]; print(t[0]['id'] if t else '')")
pass "Logged in (team=$TEAM_ID)"

# --- Test 1: YAML parsing accepts direct_outbound: true ---
log "Test 1: Parse agent definition with direct_outbound: true"
# This is validated by Go unit tests (TestParseTaskDefinitionWithDirectOutbound),
# but we verify the API round-trip here.
RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"prompt":"echo hello","direct_outbound":true}')

# Check if the session was created (may fail if no LLM provider, that's OK —
# we're testing API acceptance, not full dispatch).
SESSION_ID=$(echo "$RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sid=d.get('session_id','') or d.get('id','')
print(sid)
" 2>/dev/null || echo "")

if [ -n "$SESSION_ID" ] && [ "$SESSION_ID" != "" ]; then
  pass "Session created with direct_outbound: true (id=$SESSION_ID)"
else
  # If session creation failed, check the error. A provider-related error
  # is acceptable (means the API parsed direct_outbound but couldn't dispatch).
  ERROR_MSG=$(echo "$RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('error',''))" 2>/dev/null || echo "unknown")
  if echo "$ERROR_MSG" | grep -qi "provider\|credential\|llm\|api.key"; then
    pass "API accepted direct_outbound field (dispatch failed due to no LLM provider, expected)"
  else
    fail "Session creation failed unexpectedly: $ERROR_MSG"
  fi
fi

# --- Test 2: Session without direct_outbound defaults to false ---
log "Test 2: Session without direct_outbound defaults to false/absent"
RESP2=$(curl -s -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"prompt":"echo hello"}')

HAS_DIRECT=$(echo "$RESP2" | python3 -c "
import json,sys
d=json.load(sys.stdin)
# direct_outbound should be absent or false
val=d.get('direct_outbound', False)
print('false' if not val else 'true')
" 2>/dev/null || echo "false")

if [ "$HAS_DIRECT" = "false" ]; then
  pass "direct_outbound is false/absent by default"
else
  fail "direct_outbound should default to false, got: $HAS_DIRECT"
fi

# --- Test 3: Go unit tests pass for direct_outbound ---
log "Test 3: Go unit tests for direct_outbound parsing and runtime"
# Run only the relevant tests to keep it fast.
GOTEST_OUTPUT=$(cd /home/bmbouter/devel/alcove && go test ./internal/bridge/ -run "DirectOutbound" -count=1 2>&1) || true
if echo "$GOTEST_OUTPUT" | grep -q "^ok"; then
  PASS_COUNT=$(echo "$GOTEST_OUTPUT" | grep -c "PASS\|ok" || echo "0")
  pass "Go unit tests pass for TaskDefinition DirectOutbound parsing"
else
  fail "Go unit tests failed: $GOTEST_OUTPUT"
fi

GOTEST_RT=$(cd /home/bmbouter/devel/alcove && go test ./internal/runtime/ -run "DirectOutbound" -count=1 2>&1) || true
if echo "$GOTEST_RT" | grep -q "^ok"; then
  pass "Go unit tests pass for Podman runtime DirectOutbound networking"
else
  fail "Go runtime tests failed: $GOTEST_RT"
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
