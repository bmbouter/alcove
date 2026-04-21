# Bridge REST API Reference

Base URL: `http://<bridge-host>:8080`

All request and response bodies use `application/json`.

---

## Authentication

Protected API routes require a `Bearer` token or `Basic` authentication in the `Authorization` header. There are two authentication methods:

### Session Tokens (web login)
Obtain a session token via the login endpoint. Tokens expire after 8 hours.

```
Authorization: Bearer a1b2c3d4e5f6...
```

### Personal API Tokens (CLI/API access)
For postgres auth backend only. Create a personal API token via the dashboard or API, then use it with Basic authentication:

```
Authorization: Basic base64(username:token)
```

Where `token` is a personal API token (format: `apat_...`).

Public routes that do not require authentication:

- `POST /api/v1/auth/login`
- `GET /api/v1/health`
- `/api/v1/internal/*`

The following POST endpoints are exempt from user authentication. They are used by Skiff and Gate for internal communication:

- `POST /api/v1/sessions/{id}/transcript`
- `POST /api/v1/sessions/{id}/status`
- `POST /api/v1/sessions/{id}/proxy-log`

Rate limiting: after 5 failed login attempts within 15 minutes, the account is locked for 30 minutes.

### Team Scoping with X-Alcove-Team

Most API endpoints are scoped to a team. The auth middleware resolves the active team using the `X-Alcove-Team` request header:

```
X-Alcove-Team: <team-id>
```

- If `X-Alcove-Team` is set, the middleware validates that the authenticated user is a member of that team. On success, the resolved team ID is used for all resource queries. If the user is not a member (and not an admin), the middleware falls back to the user's personal team.
- If `X-Alcove-Team` is omitted, the user's personal team is used automatically.

Every user has a personal team created at signup. Shared teams can be created via the Teams API. Resources (sessions, workflows, credentials, catalog selections) belong to the active team and are not visible to other teams.

**curl example:**

```bash
# List sessions scoped to a specific team
curl http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Alcove-Team: 550e8400-e29b-41d4-a716-446655440000"
```

### POST /api/v1/auth/login

Authenticate and receive a session token.

**Request body:**

```json
{
  "username": "admin",
  "password": "secret"
}
```

**Response (200):**

```json
{
  "token": "a1b2c3d4e5f6...",
  "username": "admin"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Login successful |
| 400  | Invalid request body |
| 401  | Invalid credentials or account locked |
| 405  | Method not allowed (must be POST) |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "secret"}'
```

### Using the token

Pass the token in the `Authorization` header on all subsequent requests:

```bash
curl http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer a1b2c3d4e5f6..."
```

## Personal API Tokens

Personal API tokens provide a way to authenticate CLI and API requests without using your password. They are only available with the postgres auth backend.

### GET /api/v1/auth/api-tokens

List current user's personal API tokens.

**Response (200):**

```json
[
  {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "username": "admin",
    "name": "laptop CLI",
    "created_at": "2026-04-13T10:30:00Z",
    "last_accessed_at": "2026-04-13T15:45:00Z"
  }
]
```

### POST /api/v1/auth/api-tokens

Create a new personal API token.

**Request body:**

```json
{
  "name": "laptop CLI"
}
```

**Response (201):**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "username": "admin", 
  "name": "laptop CLI",
  "token": "apat_a1b2c3d4e5f6789012345678901234567890",
  "created_at": "2026-04-13T10:30:00Z"
}
```

**Important:** The `token` field is only returned once at creation time for security reasons.

### DELETE /api/v1/auth/api-tokens/{id}

Revoke a personal API token.

**Response (200):**

```json
{
  "deleted": true
}
```

### Using Personal API Tokens

Use the token as a password with Basic authentication:

```bash
curl http://localhost:8080/api/v1/sessions \
  -u "admin:apat_a1b2c3d4e5f6789012345678901234567890"
```

Or with explicit Basic auth header:

```bash
curl http://localhost:8080/api/v1/sessions \
  -H "Authorization: Basic $(echo -n 'admin:apat_a1b2c3d4e5f6789012345678901234567890' | base64)"
```

### TBR Identity Associations (rh-identity backend only)

These endpoints are only available when `AUTH_BACKEND=rh-identity`. They allow SSO users to associate Token Based Registry (TBR) identities with their account for API authentication.

#### GET /api/v1/auth/tbr-associations

List the current user's TBR identity associations.

**Response (200):**

```json
{
  "associations": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "user_id": "alice@redhat.com",
      "tbr_org_id": "12345",
      "tbr_username": "alice",
      "created_at": "2026-04-08T10:00:00Z",
      "updated_at": "2026-04-08T10:00:00Z"
    }
  ]
}
```

#### POST /api/v1/auth/tbr-associations

Create a new TBR identity association for the authenticated user.

**Request body:**

```json
{
  "tbr_org_id": "12345",
  "tbr_username": "alice"
}
```

**Response (201):**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "user_id": "alice@redhat.com", 
  "tbr_org_id": "12345",
  "tbr_username": "alice",
  "created_at": "2026-04-08T10:00:00Z",
  "updated_at": "2026-04-08T10:00:00Z"
}
```

**Status codes:**

| Code | Description |
|------|-------------|
| 201  | Association created successfully |
| 400  | Invalid request body or missing fields |
| 409  | TBR identity already associated with a user |

#### DELETE /api/v1/auth/tbr-associations/{id}

Remove a TBR identity association. Users can only delete their own associations.

**Response (200):**

```json
{
  "deleted": true
}
```

**Status codes:**

| Code | Description |
|------|-------------|
| 200  | Association deleted successfully |
| 404  | Association not found or not owned by user |

**curl examples:**

```bash
# List associations
curl http://localhost:8080/api/v1/auth/tbr-associations \
  -H "X-RH-Identity: <base64-encoded-saml-identity>"

# Create association  
curl -X POST http://localhost:8080/api/v1/auth/tbr-associations \
  -H "Content-Type: application/json" \
  -H "X-RH-Identity: <base64-encoded-saml-identity>" \
  -d '{"tbr_org_id": "12345", "tbr_username": "alice"}'

# Delete association
curl -X DELETE http://localhost:8080/api/v1/auth/tbr-associations/550e8400-e29b-41d4-a716-446655440000 \
  -H "X-RH-Identity: <base64-encoded-saml-identity>"
```

---

## Sessions (Start)

### POST /api/v1/sessions

Start a new session for execution. Bridge creates a session, dispatches it to a Skiff pod, and returns the session record.

The submitter is read from the `X-Alcove-User` header (set automatically by the auth middleware). If absent, defaults to `"anonymous"`.

**Request body:**

```json
{
  "prompt": "Fix the failing test in cmd/bridge/main_test.go",
  "repo": "https://github.com/example/myproject.git",
  "provider": "anthropic",
  "model": "claude-sonnet-4-20250514",
  "timeout": 1800,
  "budget_usd": 5.00,
  "debug": false,
  "scope": {
    "services": {
      "github": {
        "repos": ["example/myproject"],
        "operations": ["clone", "create_pr_draft", "read_prs"]
      },
      "jira": {
        "repos": ["MYPROJECT"],
        "operations": ["read_issues", "search_issues", "add_comment"]
      }
    }
  }
}
```

| Field       | Type   | Required | Default          | Description |
|-------------|--------|----------|------------------|-------------|
| `prompt`    | string | yes      |                  | The instruction for Claude Code |
| `repo`      | string | no       |                  | Git repository URL to clone |
| `provider`  | string | no       | first configured | LLM provider name |
| `model`     | string | no       | provider default | Model override |
| `timeout`   | int    | no       | 3600             | Session timeout in seconds |
| `budget_usd`| float  | no       |                  | Maximum spend for this session |
| `debug`     | bool   | no       | false            | Enable debug mode |
| `scope`     | object | no       | empty (no access)| Authorized external operations |

**Response (201):**

```json
{
  "id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "task_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "submitter": "admin",
  "prompt": "Fix the failing test in cmd/bridge/main_test.go",
  "repo": "https://github.com/example/myproject.git",
  "provider": "anthropic",
  "scope": {
    "services": {
      "github": {
        "repos": ["example/myproject"],
        "operations": ["clone", "create_pr_draft", "read_prs"]
      }
    }
  },
  "status": "running",
  "started_at": "2026-03-25T14:30:00Z"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Session dispatched and created |
| 400  | Invalid body or missing `prompt` |
| 401  | Missing or invalid token |
| 500  | Dispatch failed |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Add unit tests for the auth package",
    "repo": "https://github.com/example/myproject.git",
    "timeout": 1800
  }'
```

---

## Sessions

### GET /api/v1/sessions

List sessions with optional filters. Results are ordered by `started_at` descending.

**Query parameters:**

| Parameter  | Type   | Description |
|------------|--------|-------------|
| `status`   | string | Filter by outcome: `running`, `completed`, `error`, `timeout`, `cancelled` |
| `repo`     | string | Filter by prompt text (substring match, case-insensitive). Note: named `repo` for compatibility but searches the prompt field. |
| `since`    | string | Only sessions started at or after this timestamp (RFC 3339) |
| `until`    | string | Only sessions started at or before this timestamp (RFC 3339) |
| `page`     | int    | Page number (default: 1) |
| `per_page` | int    | Results per page, 1-100 (default: 50) |

**Response (200):**

```json
{
  "sessions": [
    {
      "id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
      "task_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "submitter": "admin",
      "prompt": "Add unit tests for the auth package",
      "provider": "anthropic",
      "scope": { "services": {} },
      "status": "completed",
      "started_at": "2026-03-25T14:30:00Z",
      "finished_at": "2026-03-25T14:45:12Z",
      "exit_code": 0,
      "duration": "15m12s",
      "artifacts": [
        { "type": "pr", "url": "https://github.com/example/myproject/pull/42" }
      ]
    }
  ],
  "count": 1,
  "total": 42,
  "page": 1,
  "per_page": 50,
  "pages": 1
}
```

**curl example:**

```bash
# List all completed sessions
curl "http://localhost:8080/api/v1/sessions?status=completed" \
  -H "Authorization: Bearer $TOKEN"

# List sessions for a specific repo since a date
curl "http://localhost:8080/api/v1/sessions?repo=myproject&since=2026-03-01T00:00:00Z" \
  -H "Authorization: Bearer $TOKEN"

# Paginate results (page 2, 20 per page)
curl "http://localhost:8080/api/v1/sessions?page=2&per_page=20" \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/sessions/{id}

Get full session detail including transcript and proxy log.

**Response (200):**

```json
{
  "id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "task_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "submitter": "admin",
  "prompt": "Add unit tests for the auth package",
  "provider": "anthropic",
  "scope": { "services": {} },
  "status": "completed",
  "started_at": "2026-03-25T14:30:00Z",
  "finished_at": "2026-03-25T14:45:12Z",
  "exit_code": 0,
  "duration": "15m12s",
  "transcript": [
    {
      "type": "assistant",
      "content": "I'll start by reading the existing tests...",
      "ts": "2026-03-25T14:30:05Z"
    }
  ],
  "proxy_log": [
    {
      "timestamp": "2026-03-25T14:31:00Z",
      "method": "POST",
      "url": "https://api.anthropic.com/v1/messages",
      "service": "anthropic",
      "decision": "allow",
      "status_code": 200,
      "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
    }
  ]
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Session found |
| 400  | Missing session ID |
| 404  | Session not found |

**curl example:**

```bash
curl http://localhost:8080/api/v1/sessions/f47ac10b-58cc-4372-a567-0e02b2c3d479 \
  -H "Authorization: Bearer $TOKEN"
```

### DELETE /api/v1/sessions/{id}

Cancel a running session or delete a completed session.

**Query parameters:**

- `action=delete` - Delete the session permanently (default is cancel)

**Cancel a running session:**

Sends a cancel signal via NATS and stops the Skiff pod.

**Response (200):**

```json
{
  "status": "cancelled",
  "session": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
}
```

**Delete a completed session:**

Permanently removes the session record, transcript, and proxy log. Only sessions in terminal states (`completed`, `error`, `timeout`, `cancelled`) can be deleted.

**Response (200):**

```json
{
  "status": "deleted",
  "session": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Session cancelled or deleted |
| 400  | Session not found, not running (for cancel), or not in terminal state (for delete) |
| 403  | Access denied (not session owner) |

**curl examples:**

```bash
# Cancel a running session
curl -X DELETE http://localhost:8080/api/v1/sessions/f47ac10b-58cc-4372-a567-0e02b2c3d479 \
  -H "Authorization: Bearer $TOKEN"

# Delete a completed session
curl -X DELETE "http://localhost:8080/api/v1/sessions/f47ac10b-58cc-4372-a567-0e02b2c3d479?action=delete" \
  -H "Authorization: Bearer $TOKEN"
```

### DELETE /api/v1/sessions

Bulk delete sessions based on criteria. Only sessions in terminal states can be deleted.

**Request body:**

```json
{
  "status": "error",
  "before": "7d"
}
```

**Parameters:**

- `status` (optional): Delete sessions with specific status (`completed`, `error`, `timeout`, `cancelled`)
- `before` (optional): Delete sessions finished before date/time (RFC3339) or duration (e.g., `7d`, `30d`)
- `ids` (optional): Array of specific session IDs to delete

**Response (200):**

```json
{
  "deleted_count": 42
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Sessions deleted successfully |
| 400  | Invalid request parameters |
| 401  | Authentication required |

**curl examples:**

```bash
# Delete all error sessions older than 7 days
curl -X DELETE http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status": "error", "before": "7d"}'

# Delete specific sessions by ID
curl -X DELETE http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"ids": ["f47ac10b-58cc-4372-a567-0e02b2c3d479", "550e8400-e29b-41d4-a716-446655440000"]}'

# Delete all completed sessions before a specific date
curl -X DELETE http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status": "completed", "before": "2023-01-01T00:00:00Z"}'
```

### GET /api/v1/sessions/{id}/transcript

Retrieve the transcript for a session. Returns the full transcript as a JSON response. Skiff flushes transcript events to the database every 5 seconds, so polling this endpoint at a similar interval provides near-real-time updates for running sessions.

**Response (200):**

```json
{
  "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "transcript": [
    {
      "type": "assistant",
      "content": "Analyzing the code...",
      "ts": "2026-03-25T14:30:05Z"
    },
    {
      "type": "tool",
      "tool": "Read",
      "input": { "file_path": "/src/main.go" },
      "ts": "2026-03-25T14:30:10Z"
    }
  ]
}
```

**Client implementation note:** The dashboard polls this endpoint every 5 seconds while the session status is `running`, and shows a live indicator. This is the same approach used for the proxy log tab. The `?stream=true` query parameter activates an SSE endpoint on the server (returns `Content-Type: text/event-stream`), but the dashboard does not use it because both `EventSource` and `fetch()+ReadableStream` are incompatible with the Akamai + Turnpike proxy chain in OpenShift staging deployments.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Transcript returned |
| 404  | Session or transcript not found |

**curl example:**

```bash
curl http://localhost:8080/api/v1/sessions/$SESSION_ID/transcript \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/sessions/{id}/proxy-log

Retrieve the Gate proxy log for a session. Returns all proxied requests with allow/deny decisions.

**Response (200):**

```json
{
  "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "proxy_log": [
    {
      "timestamp": "2026-03-25T14:31:00Z",
      "method": "POST",
      "url": "https://api.anthropic.com/v1/messages",
      "service": "anthropic",
      "decision": "allow",
      "status_code": 200,
      "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
    },
    {
      "timestamp": "2026-03-25T14:32:00Z",
      "method": "GET",
      "url": "https://api.github.com/repos/example/myproject",
      "service": "github",
      "operation": "read",
      "decision": "allow",
      "status_code": 200,
      "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
    }
  ]
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Proxy log returned |
| 404  | Session or proxy log not found |

---

## Transcript/Status Ingestion

These endpoints are called by Skiff and Gate sidecars to report data back to Bridge. They are not typically called by external clients.

### POST /api/v1/sessions/{id}/transcript

Append transcript events to a session.

**Request body:**

```json
{
  "events": [
    {
      "type": "assistant",
      "content": "I found the bug in line 42.",
      "ts": "2026-03-25T14:35:00Z"
    },
    {
      "type": "tool",
      "tool": "Edit",
      "input": { "file_path": "/src/main.go", "old_string": "foo", "new_string": "bar" },
      "ts": "2026-03-25T14:35:05Z"
    }
  ]
}
```

**Response (200):**

```json
{
  "appended": 2
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Events appended |
| 400  | Invalid body or empty events array |
| 500  | Database error |

### POST /api/v1/sessions/{id}/status

Update the outcome of a session. Typically sent by Skiff when the task finishes.

**Request body:**

```json
{
  "status": "completed",
  "exit_code": 0,
  "artifacts": [
    {
      "type": "pr",
      "url": "https://github.com/example/myproject/pull/42"
    },
    {
      "type": "commit",
      "ref": "abc1234"
    }
  ]
}
```

| Field       | Type     | Required | Description |
|-------------|----------|----------|-------------|
| `status`    | string   | yes      | One of: `completed`, `error`, `timeout`, `cancelled` |
| `exit_code` | int/null | no       | Process exit code |
| `artifacts` | array    | no       | Outputs produced by the task |

**Response (200):**

```json
{
  "updated": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Status updated |
| 400  | Invalid body or missing `status` |
| 404  | Session not found |
| 500  | Database error |

### POST /api/v1/sessions/{id}/proxy-log

Append proxy log entries to a session. Called by Gate sidecars.

**Request body:**

```json
{
  "entries": [
    {
      "timestamp": "2026-03-25T14:31:00Z",
      "method": "POST",
      "url": "https://api.anthropic.com/v1/messages",
      "service": "anthropic",
      "decision": "allow",
      "status_code": 200,
      "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
    }
  ]
}
```

**Response (200):**

```json
{
  "appended": 1
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Entries appended |
| 400  | Invalid body or empty entries array |
| 500  | Database error |

---

## Providers

### GET /api/v1/providers

List configured LLM providers.

**Response (200):**

```json
{
  "providers": [
    {
      "name": "anthropic",
      "type": "anthropic",
      "model": "claude-sonnet-4-20250514",
      "max_budget_usd": 10.0
    },
    {
      "name": "vertex",
      "type": "google-vertex",
      "model": "claude-sonnet-4-20250514",
      "max_budget_usd": 25.0
    }
  ]
}
```

**curl example:**

```bash
curl http://localhost:8080/api/v1/providers \
  -H "Authorization: Bearer $TOKEN"
```

---

## Credentials

Manage encrypted LLM provider credentials. Credential material is stored encrypted with AES-256-GCM and never returned in API responses.

### GET /api/v1/credentials

List all credentials (without secrets).

**Response (200):**

```json
{
  "credentials": [
    {
      "id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
      "name": "anthropic",
      "provider": "anthropic",
      "auth_type": "api_key",
      "project_id": "",
      "region": "",
      "created_at": "2026-03-20T10:00:00Z",
      "updated_at": "2026-03-20T10:00:00Z"
    }
  ],
  "count": 1
}
```

### POST /api/v1/credentials

Create a new credential.

**Request body:**

```json
{
  "name": "vertex-prod",
  "provider": "google-vertex",
  "auth_type": "service_account",
  "credential": "{\"type\":\"service_account\",\"project_id\":\"my-project\",...}",
  "project_id": "my-project",
  "region": "us-central1"
}
```

| Field        | Type   | Required | Description |
|--------------|--------|----------|-------------|
| `name`       | string | yes      | Display name (used for provider lookup) |
| `provider`   | string | yes      | Provider type: `anthropic`, `google-vertex`, `github`, `gitlab`, or `jira` |
| `auth_type`  | string | yes      | One of: `api_key`, `service_account`, `adc`, `pat`, `basic` |
| `credential` | string | yes      | Raw credential material (API key or JSON service account key) |
| `project_id` | string | no       | GCP project ID (Vertex only) |
| `region`     | string | no       | GCP region (Vertex only) |
| `api_host`   | string | no       | Custom API host URL (e.g., self-hosted GitLab instance) |

**Response (201):**

```json
{
  "id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
  "name": "vertex-prod",
  "provider": "google-vertex",
  "auth_type": "service_account",
  "project_id": "my-project",
  "region": "us-central1",
  "created_at": "2026-03-25T14:00:00Z",
  "updated_at": "2026-03-25T14:00:00Z"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Credential created |
| 400  | Missing required fields or invalid body |
| 500  | Storage error |

**curl examples:**

```bash
# Create an Anthropic API key credential
curl -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "anthropic",
    "provider": "anthropic",
    "auth_type": "api_key",
    "credential": "sk-ant-..."
  }'

# Create a GitHub PAT credential
curl -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "github",
    "provider": "github",
    "auth_type": "pat",
    "credential": "ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
  }'

# Create a JIRA Cloud credential (email:api_token for Basic auth)
curl -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "jira",
    "provider": "jira",
    "auth_type": "basic",
    "credential": "user@example.com:your-jira-api-token"
  }'
```

### GET /api/v1/credentials/{id}

Get a single credential's metadata (without secret material).

**Response (200):**

```json
{
  "id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
  "name": "anthropic",
  "provider": "anthropic",
  "auth_type": "api_key",
  "project_id": "",
  "region": "",
  "created_at": "2026-03-20T10:00:00Z",
  "updated_at": "2026-03-20T10:00:00Z"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Credential found |
| 404  | Credential not found |

### DELETE /api/v1/credentials/{id}

Delete a credential.

**Response (200):**

```json
{
  "deleted": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Credential deleted |
| 404  | Credential not found |

**curl example:**

```bash
curl -X DELETE http://localhost:8080/api/v1/credentials/$CRED_ID \
  -H "Authorization: Bearer $TOKEN"
```

---

## Token Refresh

### POST /api/v1/internal/token-refresh

Internal endpoint used by Gate sidecars to refresh LLM tokens. This is not protected by auth middleware. Gate calls this endpoint when an OAuth2 token (Vertex AI) nears expiry.

**Request body:**

```json
{
  "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "refresh_secret": "session-token-value"
}
```

**Response (200):**

```json
{
  "token": "ya29.a0AfH6SM...",
  "token_type": "bearer",
  "expires_in": 3600,
  "provider": "google-vertex"
}
```

For API key providers, `token_type` is `"api_key"` and `expires_in` is `0`.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Token acquired |
| 400  | Missing required fields |
| 500  | Token acquisition failed |

---

## Schedules

Schedules are defined in YAML agent definition files (via the `schedule:` field) and synced from agent repos. The API provides **read-only** access to synced schedules. To create, update, or delete schedules, edit the YAML files in your agent repos and trigger a sync.

### GET /api/v1/schedules

List all schedules.

**Response (200):**

```json
{
  "schedules": [
    {
      "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "name": "nightly-tests",
      "cron": "0 2 * * *",
      "prompt": "Run the full test suite and fix any failures",
      "repo": "https://github.com/example/myproject.git",
      "provider": "anthropic",
      "scope_preset": "",
      "timeout": 3600,
      "enabled": true,
      "last_run": "2026-03-25T02:00:00Z",
      "next_run": "2026-03-26T02:00:00Z",
      "created_at": "2026-03-20T10:00:00Z"
    }
  ],
  "count": 1
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Schedules listed |
| 405  | Method not allowed (only GET is supported) |
| 500  | Database error |

**curl example:**

```bash
curl http://localhost:8080/api/v1/schedules \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/schedules/{id}

Get a single schedule.

**Response (200):** same shape as a single item in the list response.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Schedule found |
| 404  | Schedule not found |
| 405  | Method not allowed (only GET is supported) |

---

## Users

User management is available only when Bridge uses the PostgreSQL auth backend. These endpoints are not registered when using the in-memory auth store.

### GET /api/v1/users

List all users.

**Response (200):**

```json
{
  "users": [
    {
      "username": "admin",
      "created_at": "2026-03-20T10:00:00Z"
    }
  ],
  "count": 1
}
```

### POST /api/v1/users

Create a new user.

**Request body:**

```json
{
  "username": "developer",
  "password": "strong-password-here"
}
```

**Response (201):**

```json
{
  "username": "developer",
  "created": "true"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | User created |
| 400  | Missing username or password |
| 409  | Username already exists |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/users \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username": "developer", "password": "strong-password-here"}'
```

**Note:** `GET /api/v1/users/{username}` is not supported. Only `DELETE` is available for individual users.

### DELETE /api/v1/users/{username}

Delete a user.

**Response (200):**

```json
{
  "deleted": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | User deleted |
| 404  | User not found |

### PUT /api/v1/users/{username}/password

Change a user's password.

**Request body:**

```json
{
  "password": "new-strong-password"
}
```

**Response (200):**

```json
{
  "updated": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Password changed |
| 400  | Missing password |
| 404  | User not found |

---

## Health

### GET /api/v1/health

Health check endpoint. Does not require authentication.

**Response (200):**

```json
{
  "status": "healthy",
  "runtime": "podman",
  "db": true
}
```

When the database is unreachable, returns status `503`:

```json
{
  "status": "degraded",
  "runtime": "podman",
  "db": false
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | All systems healthy |
| 503  | Database unreachable (degraded) |

**curl example:**

```bash
curl http://localhost:8080/api/v1/health
```

---

## Tools

Tools are managed via YAML definitions in agent repos. The API provides **read-only** access to synced tools. Builtin tools (`github`, `gitlab`, `jira`) are always available.

### GET /api/v1/tools

List all registered tools (builtin and custom).

**Response (200):**

```json
{
  "tools": [
    {
      "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "name": "github",
      "display_name": "GitHub",
      "tool_type": "builtin",
      "api_host": "https://api.github.com",
      "operations": [
        { "name": "clone", "description": "Clone a repository", "risk": "read" },
        { "name": "read_prs", "description": "Read pull requests", "risk": "read" },
        { "name": "create_pr_draft", "description": "Create a draft pull request", "risk": "write" }
      ],
      "created_at": "2026-03-20T10:00:00Z"
    }
  ],
  "count": 1
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Tools listed |
| 405  | Method not allowed (only GET is supported) |

**curl example:**

```bash
curl http://localhost:8080/api/v1/tools \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/tools/{name}

Get a single tool by name.

**Response (200):** same shape as a single item in the list response.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Tool found |
| 404  | Tool not found |
| 405  | Method not allowed (only GET is supported) |

**curl example:**

```bash
curl http://localhost:8080/api/v1/tools/github \
  -H "Authorization: Bearer $TOKEN"
```

---

## Security

Security profiles define per-tool access rules for sessions. They are managed via YAML definitions in agent repos (`.alcove/security-profiles/*.yml`). The API provides **read-only** access to synced profiles. To create, update, or delete profiles, edit the YAML files in your agent repos and trigger a sync.

### GET /api/v1/security-profiles

List all security profiles.

**Response (200):**

```json
{
  "profiles": [
    {
      "id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
      "name": "read-only-github",
      "display_name": "Read-Only GitHub",
      "description": "Clone and read GitHub repos, no write access",
      "source": "user",
      "tools": {
        "github": {
          "rules": [
            { "repos": ["*"], "operations": ["clone", "read_prs", "read_issues", "read_contents"] }
          ]
        }
      },
      "created_at": "2026-03-20T10:00:00Z",
      "updated_at": "2026-03-20T10:00:00Z"
    },
    {
      "id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
      "name": "repo-reader",
      "display_name": "Repo Reader",
      "description": "Read-only access to a specific repo",
      "source": "yaml",
      "source_repo": "https://github.com/org/task-definitions.git",
      "source_key": ".alcove/security-profiles/repo-reader.yml",
      "tools": {
        "github": {
          "rules": [
            { "repos": ["org/myproject"], "operations": ["clone", "read_prs", "read_contents"] }
          ]
        }
      },
      "created_at": "2026-03-20T10:00:00Z",
      "updated_at": "2026-03-20T10:00:00Z"
    }
  ],
  "count": 2
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Profiles listed |
| 405  | Method not allowed (only GET is supported) |

**curl example:**

```bash
curl http://localhost:8080/api/v1/security-profiles \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/security-profiles/{name}

Get a single profile by name.

**Response (200):** same shape as a single item in the list response.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Profile found |
| 404  | Profile not found |
| 405  | Method not allowed (only GET is supported) |

---

## Agent Repos

Configure git repositories containing YAML agent definitions (`.alcove/agents/*.yml`). Agent repos are synced automatically every 15 minutes. When a team context is active (via the `X-Alcove-Team` header), agent repos are stored as team settings. Without a team context, they fall back to per-user settings.

### GET /api/v1/user/settings/agent-repos

Get agent repos for the active team (or the current user if no team context).

**Response (200):**

```json
{
  "repos": [
    {
      "url": "https://github.com/org/task-definitions.git",
      "ref": "main",
      "name": "Org Agents",
      "enabled": true
    }
  ]
}
```

**curl example:**

```bash
curl http://localhost:8080/api/v1/user/settings/agent-repos \
  -H "Authorization: Bearer $TOKEN"
```

### PUT /api/v1/user/settings/agent-repos

Set agent repos for the active team (or the current user if no team context). Replaces the entire list and triggers an immediate sync.

**Request body:**

```json
{
  "repos": [
    {
      "url": "https://github.com/org/task-definitions.git",
      "ref": "main",
      "name": "Org Agents",
      "enabled": true
    }
  ]
}
```

**Response (200):** the saved repos array.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Repos saved |
| 400  | Invalid request body |
| 401  | Authentication required |
| 500  | Storage error |

**curl example:**

```bash
curl -X PUT http://localhost:8080/api/v1/user/settings/agent-repos \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "repos": [
      {
        "url": "https://github.com/org/task-definitions.git",
        "ref": "main",
        "name": "Org Agents"
      }
    ]
  }'
```

---

## Agent Definitions

Agent definitions are YAML files discovered from registered agent repos. They define reusable, parameterized agents.

### GET /api/v1/agent-definitions

List all agent definitions from synced agent repos.

**Response (200):**

```json
{
  "agent_definitions": [
    {
      "name": "run-tests",
      "prompt": "Run the full test suite and fix any failures",
      "repo": "https://github.com/org/myproject.git",
      "provider": "anthropic",
      "model": "claude-sonnet-4-20250514",
      "timeout": 1800,
      "budget_usd": 5.0,
      "profiles": ["read-only-github"],
      "tools": ["github"],
      "schedule": "0 2 * * *",
      "source_repo": "https://github.com/org/task-definitions.git",
      "source_file": ".alcove/agents/run-tests.yml"
    }
  ]
}
```

### GET /api/v1/agent-definitions/{name}

Get a single agent definition by name.

### POST /api/v1/agent-definitions/{name}/run

Run an agent definition immediately as a new session. Returns the created session.

**Response (201):** same shape as `POST /api/v1/sessions` (POST).

### POST /api/v1/agent-definitions/sync

Trigger an immediate sync of all agent repos (normally happens every 15 minutes).

**Response (200):**

```json
{
  "synced": true
}
```

### Event Trigger Delivery Modes

Agent definitions with event triggers (e.g., GitHub `issues.opened`) support two delivery modes via the `delivery_mode` field in the trigger configuration:

- **`polling`** (default) — Alcove polls the GitHub Events API every 60 seconds. No webhook setup required. Suitable for local development and environments without a public URL.
- **`webhook`** — GitHub pushes events to `POST /api/v1/webhooks/github`. Requires a publicly accessible Bridge URL and a configured webhook secret.

### Event Trigger Label Filtering

The trigger configuration supports an optional `labels` field (string array). When specified, the event is only dispatched if at least one of the listed labels is present on the issue or pull request. This acts as a safety gate to prevent unauthorized issues from triggering automated tasks.

```yaml
trigger:
  github:
    events: [issues]
    actions: [opened, labeled]
    repos: [org/myproject]
    labels: [ready-for-dev]
```

If `labels` is omitted or empty, all matching events are dispatched.

See [Configuration Reference](configuration.md#label-based-trigger-filtering) for full details.

### Event Trigger User Filtering

The trigger configuration supports an optional `users` field (string array). When specified, the event is only dispatched if the user who authored the comment or issue matches at least one of the listed GitHub usernames (case-insensitive). This prevents automated agents' own comments from re-triggering sessions and limits dispatch to trusted users.

```yaml
trigger:
  github:
    events: [issues, issue_comment]
    actions: [opened, created]
    repos: [org/myproject]
    labels: [ready-for-dev]
    users: [bmbouter]
```

If `users` is omitted or empty, all matching events are dispatched regardless of the event author.

See [Configuration Reference](configuration.md#user-based-trigger-filtering) for full details.

---

## Agent Templates

Starter templates for creating agent definitions.

### GET /api/v1/agent-templates

List available starter templates.

**Response (200):**

```json
{
  "templates": [
    {
      "name": "basic-task",
      "description": "A simple task with a prompt and repo",
      "raw_yaml": "name: my-task\nprompt: |\n  Your prompt here\nrepo: https://github.com/org/repo.git\ntimeout: 1800\n"
    }
  ]
}
```

---

## Admin Settings

Admin-only endpoints for system configuration. Requires the `X-Alcove-Admin: true` header (set by auth middleware for admin users).

### GET /api/v1/admin/settings/llm

Get the effective system LLM configuration (read-only). Returns the resolved configuration with source tracking for each field (`env`, `config`, or `default`). The system LLM is configured exclusively in `alcove.yaml` or via environment variables.

**Response (200):**

```json
{
  "provider": "anthropic",
  "provider_source": "env",
  "model": "claude-sonnet-4-20250514",
  "model_source": "config",
  "region": "",
  "region_source": "default",
  "project_id": "",
  "project_id_source": "default",
  "configured": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Settings returned |
| 403  | Admin access required |

**curl example:**

```bash
curl http://localhost:8080/api/v1/admin/settings/llm \
  -H "Authorization: Bearer $TOKEN"
```

### PUT /api/v1/admin/settings/llm

This endpoint returns **405 Method Not Allowed**. The system LLM configuration is read-only via the API. To change it, edit `alcove.yaml` or set `BRIDGE_LLM_*` environment variables and restart Bridge.

**Status codes:**

| Code | Meaning |
|------|---------|
| 405  | Method not allowed (system LLM is config-file-only) |

---

## Teams

Teams are the ownership unit in Alcove. Every resource belongs to a team, and every user belongs to one or more teams. A personal team is auto-created for each user at signup. Shared teams can be created for collaboration.

The `X-Alcove-Team` header scopes all API requests to a specific team (see [Team Scoping](#team-scoping-with-x-alcove-team) above).

### GET /api/v1/teams

List all teams the authenticated user is a member of.

**Response (200):**

```json
{
  "teams": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "admin's workspace",
      "is_personal": true,
      "created_at": "2026-03-20T10:00:00Z"
    },
    {
      "id": "660e8400-e29b-41d4-a716-446655440001",
      "name": "Platform Team",
      "is_personal": false,
      "created_at": "2026-04-01T10:00:00Z"
    }
  ],
  "count": 2
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Teams listed |
| 401  | Authentication required |
| 500  | Database error |

**curl example:**

```bash
curl http://localhost:8080/api/v1/teams \
  -H "Authorization: Bearer $TOKEN"
```

### POST /api/v1/teams

Create a new shared team. The authenticated user is added as the first member.

**Request body:**

```json
{
  "name": "Platform Team"
}
```

| Field  | Type   | Required | Description |
|--------|--------|----------|-------------|
| `name` | string | yes      | Team display name |

**Response (201):**

```json
{
  "id": "660e8400-e29b-41d4-a716-446655440001",
  "name": "Platform Team",
  "is_personal": false,
  "created_at": "2026-04-01T10:00:00Z",
  "members": ["admin"]
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Team created |
| 400  | Missing `name` |
| 401  | Authentication required |
| 500  | Database error |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/teams \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "Platform Team"}'
```

### GET /api/v1/teams/{id}

Get a team's details including its member list. Requires team membership or admin access.

**Response (200):**

```json
{
  "id": "660e8400-e29b-41d4-a716-446655440001",
  "name": "Platform Team",
  "is_personal": false,
  "created_at": "2026-04-01T10:00:00Z",
  "members": ["admin", "developer"]
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Team found |
| 401  | Authentication required |
| 403  | Access denied (not a member and not admin) |
| 404  | Team not found |

### PUT /api/v1/teams/{id}

Rename a team. Personal teams cannot be renamed (returns 400).

**Request body:**

```json
{
  "name": "New Team Name"
}
```

**Response (200):**

```json
{
  "updated": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Team renamed |
| 400  | Missing `name`, team not found, or personal team |
| 401  | Authentication required |
| 403  | Access denied |

### DELETE /api/v1/teams/{id}

Delete a team. Personal teams cannot be deleted (returns 400). Any running sessions for the team are cancelled before deletion.

**Response (200):**

```json
{
  "deleted": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Team deleted |
| 400  | Team not found or personal team |
| 401  | Authentication required |
| 403  | Access denied |

**curl example:**

```bash
curl -X DELETE http://localhost:8080/api/v1/teams/660e8400-e29b-41d4-a716-446655440001 \
  -H "Authorization: Bearer $TOKEN"
```

### POST /api/v1/teams/{id}/members

Add a user to a team. The user must exist. Cannot add members to personal teams.

**Request body:**

```json
{
  "username": "developer"
}
```

**Response (201):**

```json
{
  "added": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Member added |
| 400  | Missing `username`, user does not exist, or personal team |
| 401  | Authentication required |
| 403  | Access denied |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/teams/$TEAM_ID/members \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username": "developer"}'
```

### DELETE /api/v1/teams/{id}/members/{username}

Remove a user from a team. Cannot remove members from personal teams.

**Response (200):**

```json
{
  "removed": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Member removed |
| 400  | Member not found or personal team |
| 401  | Authentication required |
| 403  | Access denied |

**curl example:**

```bash
curl -X DELETE http://localhost:8080/api/v1/teams/$TEAM_ID/members/developer \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/teams/{id}/catalog

List catalog sources with item counts for the team.

**Response (200):**

```json
{
  "sources": [
    {
      "source_id": "alcove-agents",
      "name": "Alcove Agents",
      "description": "Built-in agent collection",
      "category": "agents",
      "total_items": 12,
      "enabled_items": 5
    }
  ]
}
```

**curl example:**

```bash
curl http://localhost:8080/api/v1/teams/$TEAM_ID/catalog \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/teams/{id}/catalog/{source_id}

List all items within a catalog source, with per-team enabled state.

**Query parameters:**

| Parameter | Type   | Description |
|-----------|--------|-------------|
| `search`  | string | Filter items by name or description (case-insensitive substring match) |

**Response (200):**

```json
{
  "source_id": "alcove-agents",
  "items": [
    {
      "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "source_id": "alcove-agents",
      "slug": "code-reviewer",
      "name": "Code Reviewer",
      "description": "Reviews pull requests for code quality",
      "item_type": "agent",
      "source_file": ".alcove/agents/code-reviewer.yml",
      "synced_at": "2026-04-15T10:00:00Z",
      "enabled": true
    }
  ]
}
```

### PUT /api/v1/teams/{id}/catalog/{source_id}

Bulk toggle items within a catalog source for the team.

**Request body:**

```json
{
  "items": [
    { "slug": "code-reviewer", "enabled": true },
    { "slug": "test-runner", "enabled": false }
  ]
}
```

**Response (200):**

```json
{
  "updated": true,
  "source_id": "alcove-agents",
  "count": 2
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Items updated |
| 400  | Missing or empty `items` array |
| 404  | Catalog source not found |
| 500  | Database error |

### PUT /api/v1/teams/{id}/catalog/{source_id}/{item_slug}

Toggle an individual catalog item for the team.

**Request body:**

```json
{
  "enabled": true
}
```

**Response (200):**

```json
{
  "updated": true,
  "source_id": "alcove-agents",
  "item_slug": "code-reviewer",
  "enabled": true
}
```

### POST /api/v1/teams/{id}/catalog/custom

Add a custom plugin repository to the team.

**Request body:**

```json
{
  "url": "https://github.com/org/custom-agents.git",
  "ref": "main",
  "name": "Custom Agents"
}
```

**Response (201):**

```json
{
  "added": true,
  "custom_plugins": [
    {
      "url": "https://github.com/org/custom-agents.git",
      "ref": "main",
      "name": "Custom Agents"
    }
  ]
}
```

### DELETE /api/v1/teams/{id}/catalog/custom/{index}

Remove a custom plugin repository by index.

**Response (200):**

```json
{
  "removed": true,
  "custom_plugins": []
}
```

### GET /api/v1/teams/{id}/agents

List all enabled agents (catalog items with `item_type="agent"`) for the team. Useful for workflow authoring.

**Response (200):**

```json
{
  "agents": [
    {
      "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "source_id": "alcove-agents",
      "slug": "code-reviewer",
      "name": "Code Reviewer",
      "description": "Reviews pull requests for code quality",
      "item_type": "agent",
      "source_file": ".alcove/agents/code-reviewer.yml",
      "synced_at": "2026-04-15T10:00:00Z",
      "enabled": true
    }
  ]
}
```

**curl example:**

```bash
curl http://localhost:8080/api/v1/teams/$TEAM_ID/agents \
  -H "Authorization: Bearer $TOKEN"
```

---

## Workflows

Workflows define multi-step execution graphs with agent steps (Skiff pods) and bridge steps (deterministic actions like `create-pr`, `await-ci`, `merge-pr`). Workflows are defined in YAML agent definition files and synced from agent repos.

### GET /api/v1/workflows

List all workflow definitions for the active team.

**Response (200):**

```json
{
  "workflows": [
    {
      "id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
      "name": "review-and-merge",
      "source_repo": "https://github.com/org/task-definitions.git",
      "source_file": ".alcove/agents/review-and-merge.yml",
      "team_id": "550e8400-e29b-41d4-a716-446655440000",
      "source_key": "https://github.com/org/task-definitions.git::.alcove/agents/review-and-merge.yml",
      "raw_yaml": "name: review-and-merge\nworkflow:\n  ...",
      "last_synced": "2026-04-15T10:00:00Z",
      "workflow": [
        {
          "id": "code",
          "agent": "alcove-agents/coder",
          "repo": "https://github.com/org/myproject.git",
          "outputs": ["branch"]
        },
        {
          "id": "create-pr",
          "type": "bridge",
          "action": "create-pr",
          "depends": "code",
          "inputs": {
            "repo": "org/myproject",
            "branch": "{{steps.code.outputs.branch}}",
            "base": "main",
            "title": "Automated changes"
          }
        }
      ]
    }
  ],
  "count": 1
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Workflows listed |
| 500  | Database error |

**curl example:**

```bash
curl http://localhost:8080/api/v1/workflows \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID"
```

### GET /api/v1/workflow-runs

List workflow run executions for the active team.

**Query parameters:**

| Parameter | Type   | Description |
|-----------|--------|-------------|
| `status`  | string | Filter by status: `pending`, `running`, `completed`, `failed`, `cancelled`, `awaiting_approval` |

**Response (200):**

```json
{
  "workflow_runs": [
    {
      "id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
      "workflow_id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
      "status": "running",
      "trigger_type": "manual",
      "trigger_ref": "",
      "current_step": "code",
      "step_outputs": {},
      "started_at": "2026-04-15T14:00:00Z",
      "team_id": "550e8400-e29b-41d4-a716-446655440000",
      "created_at": "2026-04-15T14:00:00Z"
    }
  ],
  "count": 1
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Runs listed |
| 500  | Database error |

**curl example:**

```bash
# List all running workflow runs
curl "http://localhost:8080/api/v1/workflow-runs?status=running" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID"
```

### POST /api/v1/workflow-runs

Trigger a new workflow run manually.

**Request body:**

```json
{
  "workflow_id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
  "trigger_ref": ""
}
```

| Field          | Type   | Required | Description |
|----------------|--------|----------|-------------|
| `workflow_id`  | string | yes      | The workflow definition ID to run |
| `trigger_ref`  | string | no       | Optional trigger reference (e.g., branch name, PR number) |

**Response (201):**

```json
{
  "id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
  "workflow_id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
  "status": "running",
  "trigger_type": "manual",
  "trigger_ref": "",
  "step_outputs": {},
  "started_at": "2026-04-15T14:00:00Z",
  "team_id": "550e8400-e29b-41d4-a716-446655440000",
  "created_at": "2026-04-15T14:00:00Z"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Workflow run created and started |
| 400  | Invalid body or missing `workflow_id` |
| 500  | Failed to start workflow run |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/workflow-runs \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890"}'
```

### GET /api/v1/workflow-runs/{id}

Get detailed information about a workflow run, including all step executions.

**Response (200):**

```json
{
  "workflow_run": {
    "id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
    "workflow_id": "b1c2d3e4-f5a6-7890-abcd-ef1234567890",
    "status": "running",
    "trigger_type": "manual",
    "trigger_ref": "",
    "current_step": "review",
    "step_outputs": {
      "code": {
        "branch": "auto/fix-tests"
      }
    },
    "started_at": "2026-04-15T14:00:00Z",
    "team_id": "550e8400-e29b-41d4-a716-446655440000",
    "created_at": "2026-04-15T14:00:00Z"
  },
  "steps": [
    {
      "id": "d3e4f5a6-b789-0123-cdef-456789012345",
      "run_id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
      "step_id": "code",
      "session_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
      "status": "completed",
      "outputs": { "branch": "auto/fix-tests" },
      "iteration": 1,
      "started_at": "2026-04-15T14:00:05Z",
      "finished_at": "2026-04-15T14:15:30Z",
      "type": "agent"
    },
    {
      "id": "e4f5a6b7-8901-2345-def0-567890123456",
      "run_id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
      "step_id": "review",
      "status": "running",
      "iteration": 1,
      "started_at": "2026-04-15T14:15:35Z",
      "type": "agent",
      "depends": "code"
    }
  ]
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Run found |
| 400  | Missing run ID |
| 404  | Run not found |

**curl example:**

```bash
curl http://localhost:8080/api/v1/workflow-runs/$RUN_ID \
  -H "Authorization: Bearer $TOKEN"
```

### POST /api/v1/workflow-runs/{id}/approve/{step_id}

Approve a workflow step that is awaiting approval. Steps with `approval: required` pause execution until explicitly approved.

**Response (200):**

```json
{
  "status": "approved",
  "run_id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
  "step_id": "deploy"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Step approved |
| 400  | Step not in awaiting_approval state or other validation error |

### POST /api/v1/workflow-runs/{id}/reject/{step_id}

Reject a workflow step that is awaiting approval. The step is marked as failed and the workflow continues based on its dependency graph.

**Response (200):**

```json
{
  "status": "rejected",
  "run_id": "c2d3e4f5-a6b7-8901-bcde-f12345678901",
  "step_id": "deploy"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Step rejected |
| 400  | Step not in awaiting_approval state or other validation error |

**curl examples:**

```bash
# Approve a step
curl -X POST http://localhost:8080/api/v1/workflow-runs/$RUN_ID/approve/deploy \
  -H "Authorization: Bearer $TOKEN"

# Reject a step
curl -X POST http://localhost:8080/api/v1/workflow-runs/$RUN_ID/reject/deploy \
  -H "Authorization: Bearer $TOKEN"
```

---

## Bridge Actions

Bridge actions are deterministic actions executed by Bridge itself (not by Skiff agents). They are used as steps in workflows with `type: bridge`. This endpoint lists the available bridge actions and their input/output schemas.

### GET /api/v1/bridge-actions

List all available bridge action schemas.

**Response (200):**

```json
{
  "actions": [
    {
      "name": "create-pr",
      "description": "Create a pull request on GitHub",
      "inputs": {
        "repo": "string (required) - Repository in owner/repo format",
        "branch": "string (required) - Source branch name",
        "base": "string (required) - Target branch name",
        "title": "string (required) - PR title",
        "body": "string (optional) - PR body/description",
        "draft": "bool (optional) - Create as draft PR"
      },
      "outputs": {
        "pr_number": "int - Pull request number",
        "pr_url": "string - Pull request URL"
      }
    },
    {
      "name": "await-ci",
      "description": "Wait for CI checks to complete on a pull request",
      "inputs": {
        "repo": "string (required) - Repository in owner/repo format",
        "pr": "int (required) - Pull request number",
        "timeout": "int (optional) - Timeout in seconds (default 900)"
      },
      "outputs": {
        "status": "string - CI result: 'passed' or 'failed'",
        "failure_logs": "string - Concatenated failure logs (if failed)",
        "failed_checks": "[]string - Names of failed checks"
      }
    },
    {
      "name": "merge-pr",
      "description": "Merge a pull request on GitHub",
      "inputs": {
        "repo": "string (required) - Repository in owner/repo format",
        "pr": "int (required) - Pull request number",
        "method": "string (optional) - Merge method: merge, squash, rebase (default merge)",
        "delete_branch": "bool (optional) - Delete source branch after merge (default true)"
      },
      "outputs": {
        "merge_sha": "string - The SHA of the merge commit"
      }
    }
  ],
  "count": 3
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Actions listed |

**curl example:**

```bash
curl http://localhost:8080/api/v1/bridge-actions \
  -H "Authorization: Bearer $TOKEN"
```

---

## Catalog

The catalog provides a registry of available sources (git repositories containing agents, plugins, LSPs, MCPs) and their items. Catalog entries are seeded from embedded data at compile time. Teams toggle individual items via the Teams catalog endpoints.

### GET /api/v1/catalog

List all catalog sources with category breakdown.

**Response (200):**

```json
{
  "entries": [
    {
      "id": "alcove-agents",
      "name": "Alcove Agents",
      "description": "Built-in agent collection for common development tasks",
      "category": "agents",
      "source_type": "git",
      "source_url": "https://github.com/bmbouter/alcove-catalog.git",
      "source_path": "agents",
      "ref": "main",
      "docs_url": "https://github.com/bmbouter/alcove-catalog",
      "tags": ["official", "agents"]
    }
  ],
  "count": 1,
  "categories": [
    { "id": "agents", "count": 1 }
  ]
}
```

Each catalog entry represents a source (a git repository or sub-path within one). Items within each source are discovered during sync and managed per-team via the `/api/v1/teams/{id}/catalog` endpoints.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Catalog listed |

**curl example:**

```bash
curl http://localhost:8080/api/v1/catalog \
  -H "Authorization: Bearer $TOKEN"
```

---

## Error Format

All error responses use the same JSON shape:

```json
{
  "error": "human-readable error message"
}
```

The `error` field is always a string. HTTP status codes follow standard conventions:

| Code | Meaning |
|------|---------|
| 400  | Bad request (invalid JSON, missing required fields) |
| 401  | Unauthorized (missing or invalid token) |
| 404  | Resource not found |
| 405  | Method not allowed |
| 409  | Conflict (duplicate resource) |
| 500  | Internal server error |
| 503  | Service unavailable (degraded health) |
