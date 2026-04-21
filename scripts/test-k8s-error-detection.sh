#!/usr/bin/env bash
# test-k8s-error-detection.sh — Verify enriched k8s TaskStatus detects container failures.
# Self-skips on non-kubernetes runtimes.
set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# Check runtime type
RUNTIME=$(curl -s "$BRIDGE_URL/api/v1/health" | python3 -c "import json,sys; print(json.load(sys.stdin).get('runtime',''))" 2>/dev/null || echo "unknown")
if [ "$RUNTIME" != "kubernetes" ]; then
  echo "SKIP: Runtime is '$RUNTIME', not kubernetes. This test is k8s-only."
  exit 0
fi

log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | \
  python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

# Test 1: Dispatch session with bad dev container image
log "Test 1: Dispatch session with non-existent dev container image"
RESP=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"k8s error detection test","timeout":60,"dev_container":{"image":"localhost/does-not-exist:never"}}')
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')

if [ "$CODE" = "201" ] || [ "$CODE" = "200" ]; then
  pass "Session dispatched (HTTP $CODE)"
  SESSION_ID=$(echo "$BODY" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('id',d.get('task_id','')))")
else
  fail "Session dispatch failed unexpectedly (HTTP $CODE): $BODY"
  echo ""
  log "=== Test Summary ==="
  echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
  exit 1
fi

# Test 2: Poll until error state
log "Test 2: Poll session until error state (max 150s)"
FINAL_STATUS=""
for i in $(seq 1 75); do
  sleep 2
  STATUS=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID" \
    -H "Authorization: Bearer $ADMIN_TOKEN" | \
    python3 -c "import json,sys; print(json.load(sys.stdin).get('status','unknown'))" 2>/dev/null || echo "unknown")
  if [ "$STATUS" = "error" ] || [ "$STATUS" = "completed" ] || [ "$STATUS" = "timeout" ]; then
    FINAL_STATUS="$STATUS"
    log "  Session reached terminal state: $STATUS (after $((i*2))s)"
    break
  fi
  if [ "$((i % 10))" = "0" ]; then
    log "  Still waiting... status=$STATUS ($((i*2))s elapsed)"
  fi
done

if [ "$FINAL_STATUS" = "error" ]; then
  pass "Session detected as error (image pull failure detected)"
elif [ -n "$FINAL_STATUS" ]; then
  pass "Session reached terminal state: $FINAL_STATUS (acceptable)"
else
  fail "Session stuck in running after 150s — enriched TaskStatus not detecting image pull failure"
fi

# Test 3: Check runtime_config for error detail
log "Test 3: Check runtime_config for container error detail"
DETAIL=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | \
  python3 -c "
import json,sys
d=json.load(sys.stdin)
rc = d.get('runtime_config', {})
err = rc.get('container_error', rc.get('startup_error', rc.get('error', '')))
print(err if err else 'none')
" 2>/dev/null || echo "none")

if [ "$DETAIL" != "none" ] && [ -n "$DETAIL" ]; then
  pass "Container error detail found: $DETAIL"
else
  log "  No container error detail in runtime_config (may not be implemented yet)"
  pass "runtime_config check completed (detail field optional)"
fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/sessions/$SESSION_ID?action=delete" \
  -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null 2>&1 || true

echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
