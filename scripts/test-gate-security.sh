#!/usr/bin/env bash
# test-gate-security.sh — End-to-end security tests for Gate proxy.
#
# Category 1 (default): Scope enforcement tests that run Gate in a container
# and verify allowed/denied operations via HTTP status codes. No real GitHub
# or GitLab tokens are needed — fake tokens are used, and the test only
# checks whether Gate returns 403 (denied) or non-403 (allowed, proxied).
#
# Category 2 (optional): Integration tests with real GitHub/GitLab APIs.
# Requires GITHUB_TOKEN and/or GITLAB_TOKEN environment variables.
#
# Usage:
#   make build-images   # build localhost/alcove-gate:dev first
#   ./scripts/test-gate-security.sh                        # scope enforcement only
#   GITHUB_TOKEN=ghp_xxx ./scripts/test-gate-security.sh   # + real GitHub API tests

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
GATE_IMAGE="localhost/alcove-gate:dev"
GATE_CONTAINER="test-gate-security"
GATE_PORT=18443
GATE_URL="http://localhost:${GATE_PORT}"

PASS_COUNT=0
FAIL_COUNT=0
RESULTS=()

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { echo ">>> $*"; }
pass() { PASS_COUNT=$((PASS_COUNT + 1)); RESULTS+=("PASS: $1"); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); RESULTS+=("FAIL: $1"); echo "  FAIL: $1"; }

# gate_request METHOD PATH [BODY]
# Returns HTTP status code.
gate_request() {
    local method="$1"
    local path="$2"
    local body="${3:-}"
    local extra_args=()

    if [[ -n "$body" ]]; then
        extra_args+=(-d "$body" -H "Content-Type: application/json")
    fi

    curl -s -o /dev/null -w "%{http_code}" \
        -X "$method" \
        "${extra_args[@]}" \
        "${GATE_URL}${path}" 2>/dev/null || echo "000"
}

# expect_allowed TEST_NAME METHOD PATH [BODY]
# Verifies Gate does NOT return 403 (operation is allowed by scope).
expect_allowed() {
    local name="$1" method="$2" path="$3" body="${4:-}"
    local code
    code=$(gate_request "$method" "$path" "$body")
    if [[ "$code" == "403" ]]; then
        fail "$name (got 403, expected non-403)"
    else
        pass "$name (HTTP $code)"
    fi
}

# expect_denied TEST_NAME METHOD PATH [BODY]
# Verifies Gate returns 403 (operation is denied by scope).
expect_denied() {
    local name="$1" method="$2" path="$3" body="${4:-}"
    local code
    code=$(gate_request "$method" "$path" "$body")
    if [[ "$code" == "403" ]]; then
        pass "$name (HTTP 403)"
    else
        fail "$name (got HTTP $code, expected 403)"
    fi
}

# ---------------------------------------------------------------------------
# Cleanup (runs on exit)
# ---------------------------------------------------------------------------
cleanup() {
    log "Cleaning up..."
    podman rm -f "$GATE_CONTAINER" 2>/dev/null || true
    log "Cleanup complete."
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Start Gate with test scope
# ---------------------------------------------------------------------------
log "Starting Gate container with test scope..."

# Remove any leftovers
podman rm -f "$GATE_CONTAINER" 2>/dev/null || true

SCOPE='{
  "services": {
    "github": {
      "repos": ["pulp/pulpcore", "pulp/pulp_rpm"],
      "operations": ["read_prs", "create_pr_draft", "read_contents", "read_issues", "read_commits", "read_branches"]
    }
  }
}'

CREDS='{"github":"fake-test-token-not-real"}'

podman run -d --rm \
    --name "$GATE_CONTAINER" \
    -p "${GATE_PORT}:8443" \
    -e GATE_SESSION_ID=test-security \
    -e GATE_SCOPE="$SCOPE" \
    -e GATE_CREDENTIALS="$CREDS" \
    -e GATE_SESSION_TOKEN=test-session-token \
    -e GATE_LLM_TOKEN=fake-llm-key \
    -e GATE_LLM_PROVIDER=anthropic \
    -e GATE_LLM_TOKEN_TYPE=api_key \
    "$GATE_IMAGE"

# Wait for Gate to be ready
log "Waiting for Gate to start..."
for i in $(seq 1 30); do
    if curl -s -o /dev/null "${GATE_URL}/healthz" 2>/dev/null; then
        break
    fi
    sleep 0.5
done

# Verify healthz
code=$(gate_request GET /healthz)
if [[ "$code" != "200" ]]; then
    echo "ERROR: Gate failed to start (healthz returned $code)"
    exit 1
fi
log "Gate is running."
echo ""

# ===========================================================================
# Category 1: Scope enforcement — GitHub allowed operations
# ===========================================================================
log "=== GitHub Scope Enforcement: Allowed Operations ==="

expect_allowed "GET /pulls on allowed repo (read_prs)" \
    GET "/github/repos/pulp/pulpcore/pulls"

expect_allowed "GET /pulls/42 on allowed repo (read_prs)" \
    GET "/github/repos/pulp/pulpcore/pulls/42"

expect_allowed "POST /pulls on allowed repo (create_pr_draft)" \
    POST "/github/repos/pulp/pulpcore/pulls" '{"title":"test","head":"branch","base":"main","draft":true}'

expect_allowed "GET /contents on allowed repo (read_contents)" \
    GET "/github/repos/pulp/pulpcore/contents/README.md"

expect_allowed "GET /issues on allowed repo (read_issues)" \
    GET "/github/repos/pulp/pulpcore/issues"

expect_allowed "GET /commits on allowed repo (read_commits)" \
    GET "/github/repos/pulp/pulpcore/commits"

expect_allowed "GET /branches on allowed repo (read_branches)" \
    GET "/github/repos/pulp/pulpcore/branches"

expect_allowed "GET /pulls on second allowed repo (pulp/pulp_rpm)" \
    GET "/github/repos/pulp/pulp_rpm/pulls"

echo ""

# ===========================================================================
# Category 1: Scope enforcement — GitHub denied operations (wrong op)
# ===========================================================================
log "=== GitHub Scope Enforcement: Denied Operations (op not in scope) ==="

expect_denied "PUT /pulls/1/merge on allowed repo (merge_pr not in scope)" \
    PUT "/github/repos/pulp/pulpcore/pulls/1/merge"

expect_denied "DELETE /git/refs/heads/branch (delete_branch not in scope)" \
    DELETE "/github/repos/pulp/pulpcore/git/refs/heads/my-branch"

expect_denied "POST /issues (create_issue not in scope)" \
    POST "/github/repos/pulp/pulpcore/issues" '{"title":"test"}'

expect_denied "PATCH /pulls/5 (update_pr not in scope)" \
    PATCH "/github/repos/pulp/pulpcore/pulls/5" '{"title":"updated"}'

expect_denied "PUT /contents/file.txt (write_contents not in scope)" \
    PUT "/github/repos/pulp/pulpcore/contents/file.txt" '{"content":"dGVzdA=="}'

expect_denied "POST /pulls/3/reviews (create_review not in scope)" \
    POST "/github/repos/pulp/pulpcore/pulls/3/reviews" '{"body":"lgtm"}'

expect_denied "POST /issues/1/comments (create_comment not in scope)" \
    POST "/github/repos/pulp/pulpcore/issues/1/comments" '{"body":"comment"}'

expect_denied "POST /actions/workflows (write_actions not in scope)" \
    POST "/github/repos/pulp/pulpcore/actions/workflows/ci.yml/dispatches"

expect_denied "POST /releases (write_releases not in scope)" \
    POST "/github/repos/pulp/pulpcore/releases" '{"tag_name":"v1.0"}'

expect_denied "DELETE /branches/feature (delete_branch not in scope)" \
    DELETE "/github/repos/pulp/pulpcore/branches/feature"

echo ""

# ===========================================================================
# Category 1: Scope enforcement — GitHub denied operations (wrong repo)
# ===========================================================================
log "=== GitHub Scope Enforcement: Denied Operations (repo not in scope) ==="

expect_denied "GET /pulls on disallowed repo (other/repo)" \
    GET "/github/repos/other/repo/pulls"

expect_denied "GET /contents on disallowed repo" \
    GET "/github/repos/evil/exfiltrate/contents/secrets.txt"

expect_denied "POST /pulls on disallowed repo" \
    POST "/github/repos/notpulp/notcore/pulls" '{"title":"test"}'

expect_denied "GET /issues on disallowed repo (different org)" \
    GET "/github/repos/notpulp/pulpcore/issues"

echo ""

# ===========================================================================
# Category 1: Scope enforcement — service not in scope
# ===========================================================================
log "=== Scope Enforcement: Service Not In Scope ==="

expect_denied "GET /gitlab MRs (gitlab not in scope)" \
    GET "/gitlab/api/v4/projects/group%2Fproject/merge_requests"

echo ""

# ===========================================================================
# Category 1: Infrastructure endpoints
# ===========================================================================
log "=== Infrastructure Endpoints ==="

code=$(gate_request GET /healthz)
if [[ "$code" == "200" ]]; then
    pass "GET /healthz returns 200"
else
    fail "GET /healthz returned $code, expected 200"
fi

echo ""

# ===========================================================================
# Category 2: Real GitHub API integration (optional)
# ===========================================================================
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    log "=== Category 2: Real GitHub API Integration ==="
    log "GITHUB_TOKEN detected — running real API tests"

    # Restart Gate with real token
    podman rm -f "$GATE_CONTAINER" 2>/dev/null || true

    REAL_CREDS="{\"github\":\"${GITHUB_TOKEN}\"}"
    REAL_SCOPE='{
      "services": {
        "github": {
          "repos": ["*"],
          "operations": ["read_prs", "read_contents", "read_issues", "read_commits"]
        }
      }
    }'

    podman run -d --rm \
        --name "$GATE_CONTAINER" \
        -p "${GATE_PORT}:8443" \
        -e GATE_SESSION_ID=test-security-real \
        -e GATE_SCOPE="$REAL_SCOPE" \
        -e GATE_CREDENTIALS="$REAL_CREDS" \
        -e GATE_SESSION_TOKEN=test-session-token \
        -e GATE_LLM_TOKEN=fake-llm-key \
        -e GATE_LLM_PROVIDER=anthropic \
        -e GATE_LLM_TOKEN_TYPE=api_key \
        "$GATE_IMAGE"

    for i in $(seq 1 30); do
        if curl -s -o /dev/null "${GATE_URL}/healthz" 2>/dev/null; then break; fi
        sleep 0.5
    done

    # Test: read a public repo's PRs (should return 200 from GitHub)
    code=$(gate_request GET "/github/repos/torvalds/linux/pulls?per_page=1")
    if [[ "$code" == "200" ]]; then
        pass "Real GitHub API: GET /pulls returns 200"
    else
        fail "Real GitHub API: GET /pulls returned $code (expected 200)"
    fi

    # Test: read repo contents (should return 200)
    code=$(gate_request GET "/github/repos/torvalds/linux/contents/README")
    if [[ "$code" == "200" ]]; then
        pass "Real GitHub API: GET /contents returns 200"
    else
        fail "Real GitHub API: GET /contents returned $code (expected 200)"
    fi

    # Test: denied operation (merge_pr not in scope) - should be 403 from Gate
    expect_denied "Real GitHub API: merge_pr denied by Gate" \
        PUT "/github/repos/torvalds/linux/pulls/1/merge"

    # Test: denied operation (write_contents not in scope)
    expect_denied "Real GitHub API: write_contents denied by Gate" \
        PUT "/github/repos/torvalds/linux/contents/test.txt" '{"content":"dGVzdA=="}'

    echo ""
else
    log "=== Category 2: Skipped (set GITHUB_TOKEN to enable) ==="
    echo ""
fi

# ===========================================================================
# Summary
# ===========================================================================
log "=== Test Summary ==="
for r in "${RESULTS[@]}"; do
    echo "  $r"
done
echo ""
echo "  Total: $((PASS_COUNT + FAIL_COUNT))  Passed: $PASS_COUNT  Failed: $FAIL_COUNT"
echo ""

if [[ "$FAIL_COUNT" -gt 0 ]]; then
    exit 1
else
    log "All tests passed."
    exit 0
fi
