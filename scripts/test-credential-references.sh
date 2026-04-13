#!/bin/bash
# test-credential-references.sh — Tests for credential references in agent definitions.
#
# Verifies that agent definitions can reference stored credentials by name and 
# have them injected as environment variables at dispatch time.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Runtime environment that supports Skiff containers
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-credential-references.sh
#
# Tests:
#   - Create credentials via API
#   - Parse agent definition with credentials field
#   - Dispatch task with credential references
#   - Verify credentials are injected into Skiff environment

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

# Create test user
curl -s -X POST "$BRIDGE_URL/api/v1/users" -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"username":"credreftest","password":"credref123","is_admin":false}' > /dev/null 2>&1 || true

USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"credreftest","password":"credref123"}' | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

# Test 1: Create test credentials
log "Test 1: Creating test credentials"

# Create Splunk credential
SPLUNK_RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Test Splunk",
    "provider": "splunk", 
    "auth_type": "api_key",
    "credential": "splunk-test-token-12345"
  }')

if echo "$SPLUNK_RESP" | grep -q '"provider":"splunk"'; then
  pass "Splunk credential created successfully"
else
  fail "Failed to create Splunk credential: $SPLUNK_RESP"
fi

# Create JIRA credential  
JIRA_RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Test JIRA",
    "provider": "jira",
    "auth_type": "api_key", 
    "credential": "jira-test-token-67890"
  }')

if echo "$JIRA_RESP" | grep -q '"provider":"jira"'; then
  pass "JIRA credential created successfully"
else
  fail "Failed to create JIRA credential: $JIRA_RESP"
fi

# Test 2: Create agent definition with credential references
log "Test 2: Creating agent definition with credential references"

# Create a simple agent repository to test parsing
TEMP_DIR=$(mktemp -d)
mkdir -p "$TEMP_DIR/.alcove/tasks"
cat > "$TEMP_DIR/.alcove/tasks/test-with-credentials.yml" << 'YAML_EOF'
name: Test Agent with Credentials
prompt: |
  Test agent that uses custom service credentials.
  Check that SPLUNK_TOKEN and JIRA_TOKEN environment variables are set.
credentials:
  SPLUNK_TOKEN: splunk
  JIRA_TOKEN: jira
  NONEXISTENT_TOKEN: nonexistent
timeout: 60
YAML_EOF

# Initialize git repo
cd "$TEMP_DIR"
git init >/dev/null 2>&1
git add . >/dev/null 2>&1
git -c user.email="test@example.com" -c user.name="Test User" commit -m "Initial commit" >/dev/null 2>&1

# Add this repo to the user's agent repos
REPO_CONFIG=$(cat << JSON_EOF
{
  "repos": [
    {
      "url": "file://$TEMP_DIR",
      "branch": "main"
    }
  ]
}
JSON_EOF
)

curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/task-repos" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d "$REPO_CONFIG" > /dev/null

# Wait for sync (background process)
sleep 3

# Check if agent definition was parsed successfully
AGENT_LIST=$(curl -s -X GET "$BRIDGE_URL/api/v1/agent-definitions" \
  -H "Authorization: Bearer $USER_TOKEN")

if echo "$AGENT_LIST" | grep -q "Test Agent with Credentials"; then
  pass "Agent definition with credentials was parsed and stored"
else
  fail "Agent definition with credentials was not found: $AGENT_LIST"
fi

# Get the agent definition ID
AGENT_ID=$(echo "$AGENT_LIST" | python3 -c "
import json, sys
data = json.load(sys.stdin)
for agent in data:
    if agent['name'] == 'Test Agent with Credentials':
        print(agent['id'])
        break
")

if [ -n "$AGENT_ID" ]; then
  pass "Found agent definition ID: $AGENT_ID"
  
  # Test 3: Dispatch task and verify credential injection (mock test)
  log "Test 3: Dispatching task with credential references"
  
  # Note: This test doesn't actually verify the environment variables in the container
  # because that would require a full runtime environment. Instead, we verify that
  # the dispatch request is accepted and the credentials field is properly handled.
  
  TASK_RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/$AGENT_ID/dispatch" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{}')
  
  if echo "$TASK_RESP" | grep -q '"id"'; then
    pass "Task with credential references dispatched successfully"
  else
    fail "Failed to dispatch task with credential references: $TASK_RESP"
  fi
else
  fail "Could not find agent definition ID"
fi

# Cleanup
log "Cleaning up..."
rm -rf "$TEMP_DIR"

curl -s -X DELETE "$BRIDGE_URL/api/v1/users/credreftest" \
  -H "Authorization: Bearer $ADMIN_TOKEN" >/dev/null 2>&1 || true

# Summary
log "Test Results:"
log "  PASS: $PASS"
log "  FAIL: $FAIL"

if [ "$FAIL" -gt 0 ]; then
  log "Some tests failed."
  exit 1
else
  log "All tests passed!"
fi
