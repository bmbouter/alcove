#!/usr/bin/env bash
# test-session-timeout.sh — Verify sessions with failing executables don't stay stuck.
set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

# Test 1: Create session with nonexistent binary
log "Test 1: Dispatch session with nonexistent executable"
RESP=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"timeout test","executable":{"url":"file:///nonexistent-test-binary-xyz"},"timeout":30}')
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')

if [ "$CODE" = "201" ] || [ "$CODE" = "200" ]; then
  pass "Session dispatched (HTTP $CODE)"
  SESSION_ID=$(echo "$BODY" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('id',d.get('task_id','')))")
elif [ "$CODE" = "500" ]; then
  pass "Session dispatch failed synchronously (HTTP 500 — runtime caught error)"
  SESSION_ID=""
else
  fail "Unexpected HTTP $CODE: $BODY"
  SESSION_ID=""
fi

# Test 2: Poll until terminal state
if [ -n "$SESSION_ID" ]; then
  log "Test 2: Poll session until terminal state (max 180s)"
  FINAL_STATUS=""
  for i in $(seq 1 90); do
    sleep 2
    STATUS=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID" \
      -H "Authorization: Bearer $ADMIN_TOKEN" | \
      python3 -c "import json,sys; print(json.load(sys.stdin).get('status','unknown'))" 2>/dev/null || echo "unknown")
    if [ "$STATUS" != "running" ] && [ "$STATUS" != "unknown" ] && [ "$STATUS" != "pending" ]; then
      FINAL_STATUS="$STATUS"
      log "  Session reached terminal state: $STATUS (after $((i*2))s)"
      break
    fi
    if [ "$((i % 15))" = "0" ]; then
      log "  Still waiting... status=$STATUS ($((i*2))s elapsed)"
    fi
  done

  if [ -n "$FINAL_STATUS" ]; then
    pass "Session reached terminal state: $FINAL_STATUS"
  else
    fail "Session stuck in running after 180s"
  fi

  # Test 3: Verify runtime_config is populated
  log "Test 3: Check runtime_config"
  RC=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID" \
    -H "Authorization: Bearer $ADMIN_TOKEN" | \
    python3 -c "import json,sys; d=json.load(sys.stdin); rc=d.get('runtime_config'); print('yes' if rc else 'no')" 2>/dev/null || echo "no")
  if [ "$RC" = "yes" ]; then
    pass "runtime_config is populated"
  else
    fail "runtime_config is empty"
  fi

  # Cleanup
  curl -s -X DELETE "$BRIDGE_URL/api/v1/sessions/$SESSION_ID?action=delete" \
    -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null 2>&1 || true
else
  pass "Session failed at dispatch (no poll needed)"
  pass "Dispatch error path handled correctly"
fi

echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
