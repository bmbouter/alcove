#!/usr/bin/env bash
# test-dev-container.sh — API-level tests for dev container support.
# Requires a running Bridge instance with credentials and alcove-testing configured.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
TOKEN="${TOKEN:-$(curl -s "$BASE_URL/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")}"

if [ -z "$TOKEN" ]; then
  echo "FAIL: Could not get auth token"
  exit 1
fi

AUTH="Authorization: Bearer $TOKEN"
PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL+1)); }

echo "=== Dev Container Support Tests ==="
echo ""

# Test 1: dev_container field appears in agent definitions API
echo "--- Test 1: Agent definition has dev_container field ---"
AGENT_DEF=$(curl -s "$BASE_URL/api/v1/agent-definitions" -H "$AUTH" | \
  python3 -c "
import sys, json
data = json.load(sys.stdin)
for item in data.get('agent_definitions', []):
    if isinstance(item, dict) and item.get('name') == 'Dev Container Test':
        print(json.dumps(item))
        break
")

if [ -z "$AGENT_DEF" ]; then
  fail "Dev Container Test agent definition not found"
else
  IMAGE=$(echo "$AGENT_DEF" | python3 -c "
import sys, json
d = json.load(sys.stdin)
dc = d.get('dev_container')
print(dc.get('image', '') if dc else '')
")
  if [ "$IMAGE" = "docker.io/library/python:3.11-slim" ]; then
    pass "dev_container.image = $IMAGE"
  else
    fail "Expected python:3.11-slim, got '$IMAGE'"
  fi
fi

# Test 2: Agent definition without dev_container has null/missing field
echo ""
echo "--- Test 2: Agent definition without dev_container ---"
TEST_TASK=$(curl -s "$BASE_URL/api/v1/agent-definitions" -H "$AUTH" | \
  python3 -c "
import sys, json
data = json.load(sys.stdin)
for item in data.get('agent_definitions', []):
    if isinstance(item, dict) and item.get('name') == 'Test Task':
        dc = item.get('dev_container')
        print('none' if dc is None else json.dumps(dc))
        break
")
if [ "$TEST_TASK" = "none" ]; then
  pass "Test Task has no dev_container (null)"
else
  fail "Test Task should have null dev_container, got: $TEST_TASK"
fi

# Test 3: Shim binary builds and runs
echo ""
echo "--- Test 3: Shim binary integration test ---"
if [ ! -f "./bin/shim" ]; then
  fail "bin/shim not found"
else
  SHIM_TOKEN=e2e-test SHIM_PORT=19092 ./bin/shim &
  SHIM_PID=$!
  sleep 1

  HEALTH=$(curl -s http://localhost:19092/healthz)
  if echo "$HEALTH" | grep -q '"ok"'; then
    pass "Shim healthz returns ok"
  else
    fail "Shim healthz unexpected: $HEALTH"
  fi

  EXEC_RESULT=$(curl -s -X POST http://localhost:19092/exec \
    -H "Authorization: Bearer e2e-test" \
    -H "Content-Type: application/json" \
    -d '{"cmd": "echo hello-e2e", "timeout": 5}')
  if echo "$EXEC_RESULT" | grep -q 'hello-e2e'; then
    pass "Shim exec returns command output"
  else
    fail "Shim exec unexpected: $EXEC_RESULT"
  fi

  EXIT_CODE=$(echo "$EXEC_RESULT" | grep '"exit"' | python3 -c "import sys,json; print(json.load(sys.stdin).get('code','?'))")
  if [ "$EXIT_CODE" = "0" ]; then
    pass "Shim exec exit code is 0"
  else
    fail "Expected exit code 0, got $EXIT_CODE"
  fi

  AUTH_REJECT=$(curl -s -w "%{http_code}" -o /dev/null -X POST http://localhost:19092/exec \
    -H "Authorization: Bearer wrong" \
    -H "Content-Type: application/json" \
    -d '{"cmd": "echo fail"}')
  if [ "$AUTH_REJECT" = "401" ]; then
    pass "Shim rejects bad auth with 401"
  else
    fail "Expected 401, got $AUTH_REJECT"
  fi

  kill $SHIM_PID 2>/dev/null
  wait $SHIM_PID 2>/dev/null || true
fi

# Test 4: Makefile builds shim binary
echo ""
echo "--- Test 4: Makefile includes shim ---"
if grep -q 'CMDS.*shim' Makefile; then
  pass "Makefile CMDS includes shim"
else
  fail "Makefile CMDS missing shim"
fi

if ! grep -q 'SHIM_BIN_PATH' Makefile; then
  pass "Makefile does not pass SHIM_BIN_PATH (shim baked into image)"
else
  fail "Makefile still references SHIM_BIN_PATH (should be removed)"
fi

# Test 5: Shim binary is statically linked
echo ""
echo "--- Test 5: Shim binary ---"
if [ -f "./bin/shim" ]; then
  if file ./bin/shim | grep -q "statically linked"; then
    pass "bin/shim is statically linked"
  else
    fail "bin/shim is not statically linked"
  fi
else
  fail "bin/shim not found"
fi

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
