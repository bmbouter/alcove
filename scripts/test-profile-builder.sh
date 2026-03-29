#!/bin/bash
set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0
WARN=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN: $*"; WARN=$((WARN+1)); }

check_ops() {
    local label="$1" tool="$2" expected_ops="$3" actual="$4"
    for op in $expected_ops; do
        if echo "$actual" | python3 -c "import json,sys; d=json.load(sys.stdin); ops=d.get('profile',d).get('tools',{}).get('$tool',{}).get('operations',[]); sys.exit(0 if '$op' in ops else 1)" 2>/dev/null; then
            pass "$label: has $tool/$op"
        else
            fail "$label: missing $tool/$op"
        fi
    done
}

check_repos() {
    local label="$1" tool="$2" expected_repo="$3" actual="$4"
    if echo "$actual" | python3 -c "import json,sys; d=json.load(sys.stdin); repos=d.get('profile',d).get('tools',{}).get('$tool',{}).get('repos',[]); sys.exit(0 if '$expected_repo' in repos else 1)" 2>/dev/null; then
        pass "$label: $tool repos contain $expected_repo"
    else
        fail "$label: $tool repos missing $expected_repo"
    fi
}

# Setup
log "Setting up..."
TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

# Check if profile builder is available
AVAIL=$(curl -s -X POST "$BRIDGE_URL/api/v1/profiles/build" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description":"test"}' -w "\n%{http_code}" | tail -1)
if [ "$AVAIL" = "503" ]; then
    echo "SKIP: System LLM not configured. Set BRIDGE_LLM_PROVIDER to run these tests."
    exit 0
fi

build_profile() {
    curl -s -X POST "$BRIDGE_URL/api/v1/profiles/build" \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/json" \
      -d "{\"description\":\"$1\"}"
}

# --- Test 1: Simple GitHub read-only ---
log "Test 1: Simple GitHub read-only"
R=$(build_profile "Read-only access to pulp/pulpcore on GitHub")
check_ops "T1" "github" "clone read_contents read_prs" "$R"
check_repos "T1" "github" "pulp/pulpcore" "$R"

# --- Test 2: GitHub contributor ---
log "Test 2: GitHub contributor with PR"
R=$(build_profile "Clone pulp/pulpcore, push a branch, and open a draft PR on GitHub")
check_ops "T2" "github" "clone push_branch create_pr_draft" "$R"
check_repos "T2" "github" "pulp/pulpcore" "$R"

# --- Test 3: Self-hosted GitLab ---
log "Test 3: Self-hosted GitLab MR"
R=$(build_profile "Open an MR and comment on it for service/app-interface on gitlab.cee.redhat.com")
check_ops "T3" "gitlab" "clone create_mr create_comment" "$R"
check_repos "T3" "gitlab" "service/app-interface" "$R"

# --- Test 4: Mixed GitHub + GitLab ---
log "Test 4: Mixed GitHub + GitLab"
R=$(build_profile "Read all GitHub repos. Open PRs on pulp/pulp-service on GitHub. Open MRs on service/app-interface on GitLab.")
check_ops "T4" "github" "clone read_contents read_prs" "$R"
check_ops "T4" "gitlab" "clone create_mr" "$R"
check_repos "T4" "gitlab" "service/app-interface" "$R"

# --- Test 5: Wildcard read + specific write ---
log "Test 5: Wildcard read + specific write"
R=$(build_profile "Read code from any GitHub repo. Push branches and open draft PRs only on pulp/pulpcore.")
check_ops "T5" "github" "clone read_contents" "$R"

# --- Test 6: Full maintainer ---
log "Test 6: Full maintainer access"
R=$(build_profile "Full maintainer access to pulp/pulpcore on GitHub including merging PRs and deleting branches")
check_ops "T6" "github" "clone push_branch create_pr merge_pr" "$R"
check_repos "T6" "github" "pulp/pulpcore" "$R"

# --- Test 7: Comment only ---
log "Test 7: Comment on PRs only"
R=$(build_profile "Only comment on pull requests on pulp/pulp-service on GitHub, nothing else")
check_ops "T7" "github" "create_comment" "$R"
check_repos "T7" "github" "pulp/pulp-service" "$R"

# --- Test 8: The user's exact prompt ---
log "Test 8: User's exact complex prompt"
R=$(build_profile "Allow code reading, opening a PR, and commenting on PRs against https://github.com/pulp/pulp-service/. Allow all code reading from any github repo. Allow opening an MR and commenting on an MR for https://gitlab.cee.redhat.com/service/app-interface.")
check_ops "T8" "github" "clone read_contents create_pr create_comment" "$R"
check_ops "T8" "gitlab" "clone create_mr create_comment" "$R"
check_repos "T8" "gitlab" "service/app-interface" "$R"

# --- Test 9: Profile CRUD roundtrip ---
log "Test 9: Save and retrieve generated profile"
R=$(build_profile "Read-only access to all GitLab repos")
PROFILE_NAME=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin).get('profile',json.load(open('/dev/stdin')) if False else json.load(sys.stdin)).get('name','test-rt'))" 2>/dev/null || echo "test-roundtrip")
# Save it
SAVE_RESULT=$(echo "$R" | python3 -c "
import json,sys
d = json.load(sys.stdin)
p = d.get('profile', d)
print(json.dumps(p))
" | curl -s -X POST "$BRIDGE_URL/api/v1/profiles" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d @-)
SAVE_ID=$(echo "$SAVE_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))" 2>/dev/null)
if [ "$SAVE_ID" != "ERROR" ] && [ -n "$SAVE_ID" ]; then
    pass "T9: Saved generated profile"
    # Clean up
    curl -s -X DELETE "$BRIDGE_URL/api/v1/profiles/$PROFILE_NAME" -H "Authorization: Bearer $TOKEN" > /dev/null 2>&1
else
    fail "T9: Failed to save generated profile"
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL  Warnings: $WARN"
if [ "$FAIL" -gt 0 ]; then
    echo "  NOTE: Profile builder uses AI — some failures may be non-deterministic"
    exit 1
else
    echo "  All tests passed."
fi
