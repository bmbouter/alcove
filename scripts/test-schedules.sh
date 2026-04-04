#!/bin/bash
# test-schedules.sh — Tests for the schedule management API.
#
# Verifies CRUD operations for scheduled tasks, including create, list, get,
# update, enable/disable, delete, and validation of cron expressions.
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
#   - Create/list/get/update/delete schedules
#   - Enable/disable toggle
#   - Cross-user schedule isolation
#   - Invalid cron expression rejection
#   - Debug field roundtrip

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

# Create alice if needed
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"sched-alice","password":"schedalice123","is_admin":false}' > /dev/null 2>&1 || true

curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"sched-bob","password":"schedbob1234","is_admin":false}' > /dev/null 2>&1 || true

ALICE_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"sched-alice","password":"schedalice123"}' | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

BOB_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"sched-bob","password":"schedbob1234"}' | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

# --- Test 1: Create schedule ---
log "Test 1: Create schedule"
SCHED=$(curl -s -X POST "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice hourly","cron":"0 * * * *","prompt":"test hourly","provider":"vertex","timeout":60,"enabled":true}')
SCHED_ID=$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$SCHED_ID" != "ERROR" ]; then
  pass "Created schedule: $SCHED_ID"
else
  fail "Failed to create schedule: $SCHED"
fi

# --- Test 2: List schedules ---
log "Test 2: List schedules"
COUNT=$(curl -s "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('count',0))")
if [ "$COUNT" -gt 0 ]; then
  pass "Alice can list her schedules (count=$COUNT)"
else
  fail "Alice has no schedules"
fi

# --- Test 3: Get schedule ---
log "Test 3: Get schedule"
NAME=$(curl -s "$BRIDGE_URL/api/v1/schedules/$SCHED_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "import json,sys; print(json.load(sys.stdin).get('name',''))")
if [ "$NAME" = "Alice hourly" ]; then
  pass "Got schedule by ID"
else
  fail "Schedule name mismatch: $NAME"
fi

# --- Test 4: Update schedule ---
log "Test 4: Update schedule"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/schedules/$SCHED_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice updated","cron":"0 2 * * *","prompt":"updated prompt","provider":"vertex","timeout":120,"enabled":true}')
if [ "$HTTP_CODE" = "200" ]; then
  pass "Updated schedule"
else
  fail "Update returned $HTTP_CODE"
fi

# --- Test 5: Enable/disable ---
log "Test 5: Enable/disable"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/schedules/$SCHED_ID/enable" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":false}')
if [ "$HTTP_CODE" = "200" ]; then
  pass "Disabled schedule"
else
  fail "Enable/disable returned $HTTP_CODE"
fi

# --- Test 6: User isolation ---
log "Test 6: User isolation"
BOB_COUNT=$(curl -s "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $BOB_TOKEN" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('count',0))")
if [ "$BOB_COUNT" = "0" ]; then
  pass "Bob cannot see Alice's schedules"
else
  fail "Bob can see $BOB_COUNT schedules (should be 0)"
fi

BOB_GET=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/schedules/$SCHED_ID" \
  -H "Authorization: Bearer $BOB_TOKEN")
if [ "$BOB_GET" = "404" ]; then
  pass "Bob gets 404 on Alice's schedule"
else
  fail "Bob gets $BOB_GET on Alice's schedule (expected 404)"
fi

# --- Test 7: Invalid cron ---
log "Test 7: Invalid cron expression"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Bad","cron":"invalid","prompt":"test","enabled":true}')
if [ "$HTTP_CODE" = "500" ] || [ "$HTTP_CODE" = "400" ]; then
  pass "Invalid cron rejected ($HTTP_CODE)"
else
  fail "Invalid cron returned $HTTP_CODE (expected 400/500)"
fi

# --- Test 8: Delete ---
log "Test 8: Delete schedule"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/schedules/$SCHED_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$HTTP_CODE" = "200" ]; then
  pass "Deleted schedule"
else
  fail "Delete returned $HTTP_CODE"
fi

# Verify deletion
AFTER_COUNT=$(curl -s "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "import json,sys; d=json.load(sys.stdin); ids=[s['id'] for s in (d.get('schedules') or [])]; print('found' if '$SCHED_ID' in ids else 'gone')")
if [ "$AFTER_COUNT" = "gone" ]; then
  pass "Schedule confirmed deleted"
else
  fail "Schedule still exists after deletion"
fi

# --- Test 9: Debug field roundtrip ---
log "Test 9: Debug field"
DEBUG_SCHED=$(curl -s -X POST "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Debug test","cron":"0 * * * *","prompt":"debug test","debug":true,"enabled":false}')
DEBUG_ID=$(echo "$DEBUG_SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
DEBUG_VAL=$(curl -s "$BRIDGE_URL/api/v1/schedules/$DEBUG_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "import json,sys; print(json.load(sys.stdin).get('debug',False))")
if [ "$DEBUG_VAL" = "True" ]; then
  pass "Debug field roundtrip works"
else
  fail "Debug field is $DEBUG_VAL (expected True)"
fi
# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/schedules/$DEBUG_ID" -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
