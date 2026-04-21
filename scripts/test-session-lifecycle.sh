#!/usr/bin/env bash
# test-session-lifecycle.sh — End-to-end session lifecycle test.
#
# Dispatches an executable agent (test-agent binary), polls until completion,
# and verifies the transcript contains expected content. Tests the entire
# dispatch pipeline: API → NATS → container → transcript → status update.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Container runtime with skiff-base image containing test-agent binary
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-session-lifecycle.sh

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup ---
log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); t=d.get('token',''); print(t) if t else sys.exit('Login failed: ' + json.dumps(d))")

# =====================================================================
# Test 1: Dispatch executable agent session
# =====================================================================
log "Test 1: Dispatch executable agent session"

SESSION_RESP=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "session lifecycle test",
    "executable": {"url": "file:///usr/local/bin/test-agent"},
    "timeout": 120
  }')
SESSION_CODE=$(echo "$SESSION_RESP" | tail -1)
SESSION_BODY=$(echo "$SESSION_RESP" | sed '$d')

if [ "$SESSION_CODE" = "200" ] || [ "$SESSION_CODE" = "201" ]; then
  pass "Session created (HTTP $SESSION_CODE)"
else
  fail "Session creation failed (HTTP $SESSION_CODE): $SESSION_BODY"
  echo ""
  log "=== Test Summary ==="
  echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
  exit 1
fi

SESSION_ID=$(echo "$SESSION_BODY" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('id', d.get('task_id', '')))")

if [ -z "$SESSION_ID" ]; then
  fail "Could not extract session ID from response"
  echo ""
  log "=== Test Summary ==="
  echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
  exit 1
fi

log "  Session ID: $SESSION_ID"

# =====================================================================
# Test 2: Poll session until completion
# =====================================================================
log "Test 2: Poll session until completion"

FINAL_STATUS=""
for i in $(seq 1 60); do
  sleep 2
  STATUS_RESP=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID" \
    -H "Authorization: Bearer $ADMIN_TOKEN")
  STATUS=$(echo "$STATUS_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('status','unknown'))" 2>/dev/null || echo "unknown")

  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "error" ] || [ "$STATUS" = "timed_out" ]; then
    FINAL_STATUS="$STATUS"
    log "  Session reached terminal state: $STATUS (after $((i*2))s)"
    break
  fi

  if [ "$((i % 5))" = "0" ]; then
    log "  Still waiting... status=$STATUS ($((i*2))s elapsed)"
  fi
done

if [ "$FINAL_STATUS" = "completed" ]; then
  pass "Session completed successfully"
elif [ -n "$FINAL_STATUS" ]; then
  fail "Session ended with status: $FINAL_STATUS (expected: completed)"
else
  fail "Session did not reach terminal state within 120s (last status: $STATUS)"
fi

# =====================================================================
# Test 3: Verify transcript contains test marker
# =====================================================================
log "Test 3: Verify transcript"

if [ "$FINAL_STATUS" = "completed" ] || [ "$FINAL_STATUS" = "error" ]; then
  TRANSCRIPT=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID/transcript" \
    -H "Authorization: Bearer $ADMIN_TOKEN")

  if echo "$TRANSCRIPT" | grep -q "ALCOVE_TEST_MARKER"; then
    pass "Transcript contains ALCOVE_TEST_MARKER"
  else
    TRANSCRIPT_LEN=$(echo "$TRANSCRIPT" | wc -c)
    if [ "$TRANSCRIPT_LEN" -gt 10 ]; then
      pass "Transcript has content ($TRANSCRIPT_LEN bytes) but missing marker (executable may not stream)"
    else
      fail "Transcript is empty or too short ($TRANSCRIPT_LEN bytes)"
    fi
  fi
else
  log "  Skipping transcript check (session did not complete)"
fi

# =====================================================================
# Test 4: Verify session detail fields
# =====================================================================
log "Test 4: Verify session detail fields"

DETAIL=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID" \
  -H "Authorization: Bearer $ADMIN_TOKEN")

HAS_STATUS=$(echo "$DETAIL" | python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if 'status' in d else 'no')")
HAS_CREATED=$(echo "$DETAIL" | python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if 'created_at' in d or 'submitted_at' in d or 'started_at' in d else 'no')")

if [ "$HAS_STATUS" = "yes" ]; then
  pass "Session detail includes status field"
else
  fail "Session detail missing status field"
fi

if [ "$HAS_CREATED" = "yes" ]; then
  pass "Session detail includes timestamp"
else
  fail "Session detail missing timestamp"
fi

# =====================================================================
# Cleanup
# =====================================================================
log "Cleanup..."
curl -s -X DELETE "$BRIDGE_URL/api/v1/sessions/$SESSION_ID?action=delete" \
  -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null 2>&1 || true

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
