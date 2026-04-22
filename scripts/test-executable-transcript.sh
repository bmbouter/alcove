#!/usr/bin/env bash
# test-executable-transcript.sh — Verify executable agents capture stdout and stderr in transcripts.
#
# Creates a local git repo with an agent definition YAML that uses the
# test-agent binary (baked into skiff-base), syncs it as an agent repo,
# dispatches a session, and verifies the transcript contains both stdout
# and stderr content with proper stream annotations.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Container runtime with skiff-base image containing test-agent binary
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-executable-transcript.sh

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

# Create test user
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"transcript-tester","password":"transcript123","is_admin":false}' > /dev/null 2>&1 || true

USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"transcript-tester","password":"transcript123"}' | \
  python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

TEAM_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" | \
  python3 -c "import json,sys; teams=json.load(sys.stdin).get('teams',[]); print(next((t['id'] for t in teams if t.get('is_personal')), ''))")

log "  User token: ${USER_TOKEN:0:10}..."
log "  Team ID: ${TEAM_ID:0:10}..."

# =====================================================================
# Step 1: Create a local git repo with an executable agent definition
# =====================================================================
log "Step 1: Create local git repo with executable agent definition"

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

REPO_DIR="$TMPDIR/test-exec-repo"
mkdir -p "$REPO_DIR/.alcove/agents"

cat > "$REPO_DIR/.alcove/agents/test-executable.yml" <<'YAML'
name: Executable Transcript Test
description: |
  Test agent that writes to both stdout and stderr.
  Used to verify transcript capture of both output streams.

executable:
  url: "file:///usr/local/bin/test-agent"

timeout: 120
YAML

# Initialize a bare git repo so Bridge can clone it via file:// URL
(cd "$REPO_DIR" && git init -b main && git config user.email "test@test.com" && git config user.name "Test" && git add -A && git commit -m "Add executable agent definition" --quiet)

REPO_URL="file://$REPO_DIR"
log "  Local repo: $REPO_URL"

# =====================================================================
# Step 2: Configure agent repo and sync
# =====================================================================
log "Step 2: Configure agent repo and sync"

REPO_RESP=$(curl -s -w "\n%{http_code}" -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d "{\"repos\":[{\"url\":\"$REPO_URL\",\"name\":\"test-exec\"}]}")
REPO_CODE=$(echo "$REPO_RESP" | tail -1)

if [ "$REPO_CODE" = "200" ] || [ "$REPO_CODE" = "204" ]; then
  pass "Agent repo configured (HTTP $REPO_CODE)"
else
  fail "Agent repo configuration failed (HTTP $REPO_CODE)"
fi

curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID" > /dev/null

# Wait for sync to complete
SYNC_COUNT=0
for attempt in $(seq 1 10); do
  sleep 2
  SYNC_COUNT=$(curl -s "$BRIDGE_URL/api/v1/agent-definitions" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "X-Alcove-Team: $TEAM_ID" | \
    python3 -c "import json,sys; print(len(json.load(sys.stdin).get('agent_definitions',[])))" 2>/dev/null || echo "0")
  if [ "$SYNC_COUNT" -gt 0 ]; then break; fi
done

if [ "$SYNC_COUNT" -gt 0 ]; then
  pass "Synced $SYNC_COUNT agent definitions"
else
  fail "No agent definitions synced"
  echo ""
  log "=== Test Summary ==="
  echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
  exit 1
fi

# =====================================================================
# Test 3: Verify the executable agent definition was parsed correctly
# =====================================================================
log "Test 3: Verify executable agent definition"

DEFS=$(curl -s "$BRIDGE_URL/api/v1/agent-definitions" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID")

EXEC_DEF=$(echo "$DEFS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
for d in data.get('agent_definitions', []):
    if d.get('name') == 'Executable Transcript Test':
        print(json.dumps(d))
        sys.exit()
print('')
")

if [ -n "$EXEC_DEF" ]; then
  pass "Found 'Executable Transcript Test' agent definition"

  EXEC_URL=$(echo "$EXEC_DEF" | python3 -c "
import json, sys
d = json.load(sys.stdin)
e = d.get('executable', {})
print(e.get('url', '') if e else '')
")
  if [ "$EXEC_URL" = "file:///usr/local/bin/test-agent" ]; then
    pass "Executable URL correct: $EXEC_URL"
  else
    fail "Executable URL wrong: $EXEC_URL (expected file:///usr/local/bin/test-agent)"
  fi
else
  fail "Executable Transcript Test agent definition not found after sync"
fi

# =====================================================================
# Test 4: Dispatch session using the executable agent
# =====================================================================
log "Test 4: Dispatch executable agent session"

SESSION_RESP=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{
    "prompt": "executable transcript test",
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
log "  Session ID: $SESSION_ID"

# =====================================================================
# Test 5: Poll session until completion
# =====================================================================
log "Test 5: Poll session until completion"

FINAL_STATUS=""
for i in $(seq 1 60); do
  sleep 2
  STATUS_RESP=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "X-Alcove-Team: $TEAM_ID")
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
# Test 6: Verify transcript contains stdout content
# =====================================================================
log "Test 6: Verify transcript stdout content"

if [ "$FINAL_STATUS" = "completed" ] || [ "$FINAL_STATUS" = "error" ]; then
  TRANSCRIPT=$(curl -s "$BRIDGE_URL/api/v1/sessions/$SESSION_ID/transcript" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "X-Alcove-Team: $TEAM_ID")

  if echo "$TRANSCRIPT" | grep -q "ALCOVE_TEST_MARKER"; then
    pass "Transcript contains ALCOVE_TEST_MARKER (stdout captured)"
  else
    fail "Transcript does not contain ALCOVE_TEST_MARKER (stdout not captured)"
  fi
else
  log "  Skipping transcript check (session did not complete)"
fi

# =====================================================================
# Test 7: Verify transcript contains stderr content
# =====================================================================
log "Test 7: Verify transcript stderr content"

if [ "$FINAL_STATUS" = "completed" ] || [ "$FINAL_STATUS" = "error" ]; then
  if echo "$TRANSCRIPT" | grep -q "STDERR_MARKER"; then
    pass "Transcript contains STDERR_MARKER (stderr captured)"
  else
    fail "Transcript does not contain STDERR_MARKER (stderr not captured)"
  fi
else
  log "  Skipping stderr check (session did not complete)"
fi

# =====================================================================
# Test 8: Verify stderr events have "stream": "stderr" JSON field
# =====================================================================
log "Test 8: Verify stderr stream annotation in transcript JSON"

if [ "$FINAL_STATUS" = "completed" ] || [ "$FINAL_STATUS" = "error" ]; then
  STDERR_STREAM_COUNT=$(echo "$TRANSCRIPT" | python3 -c "
import json, sys
data = json.load(sys.stdin)
events = data if isinstance(data, list) else data.get('events', data.get('transcript', []))
stderr_events = [e for e in events if isinstance(e, dict) and e.get('stream') == 'stderr']
print(len(stderr_events))
" 2>/dev/null || echo "0")

  if [ "$STDERR_STREAM_COUNT" -gt 0 ]; then
    pass "Found $STDERR_STREAM_COUNT transcript events with \"stream\": \"stderr\""
  else
    fail "No transcript events have \"stream\": \"stderr\" field"
  fi

  # Verify stdout events do NOT have the stream field (backward compatibility)
  STDOUT_STREAM_CHECK=$(echo "$TRANSCRIPT" | python3 -c "
import json, sys
data = json.load(sys.stdin)
events = data if isinstance(data, list) else data.get('events', data.get('transcript', []))
stdout_with_stream = [e for e in events if isinstance(e, dict)
                      and 'ALCOVE_TEST_MARKER' in e.get('content', '')
                      and 'stream' in e]
print(len(stdout_with_stream))
" 2>/dev/null || echo "0")

  if [ "$STDOUT_STREAM_CHECK" = "0" ]; then
    pass "Stdout events do not have \"stream\" field (backward compatible)"
  else
    fail "Stdout events unexpectedly have \"stream\" field ($STDOUT_STREAM_CHECK events)"
  fi
else
  log "  Skipping stream annotation check (session did not complete)"
fi

# =====================================================================
# Cleanup
# =====================================================================
log "Cleanup..."
curl -s -X DELETE "$BRIDGE_URL/api/v1/sessions/$SESSION_ID?action=delete" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID" > /dev/null 2>&1 || true
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"repos":[]}' > /dev/null
curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID" > /dev/null

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
