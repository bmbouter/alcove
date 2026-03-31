# Bridge REST API Reference

Base URL: `http://<bridge-host>:8080`

All request and response bodies use `application/json`.

---

## Authentication

Protected API routes require a `Bearer` token in the `Authorization` header. Obtain a token via the login endpoint. Tokens expire after 8 hours.

Public routes that do not require authentication:

- `POST /api/v1/auth/login`
- `GET /api/v1/health`
- `/api/v1/internal/*`

The following POST endpoints are exempt from user authentication. They are used by Skiff and Gate for internal communication:

- `POST /api/v1/sessions/{id}/transcript`
- `POST /api/v1/sessions/{id}/status`
- `POST /api/v1/sessions/{id}/proxy-log`

Rate limiting: after 5 failed login attempts within 15 minutes, the account is locked for 30 minutes.

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

---

## Tasks

### POST /api/v1/tasks

Submit a new task for execution. Bridge creates a session, dispatches the task to a Skiff pod, and returns the session record.

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
      }
    }
  }
}
```

| Field       | Type   | Required | Default          | Description |
|-------------|--------|----------|------------------|-------------|
| `prompt`    | string | yes      |                  | The task instruction for Claude Code |
| `repo`      | string | no       |                  | Git repository URL to clone |
| `provider`  | string | no       | first configured | LLM provider name |
| `model`     | string | no       | provider default | Model override |
| `timeout`   | int    | no       | 3600             | Task timeout in seconds |
| `budget_usd`| float  | no       |                  | Maximum spend for this task |
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
| 201  | Task dispatched, session created |
| 400  | Invalid body or missing `prompt` |
| 401  | Missing or invalid token |
| 500  | Dispatch failed |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
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

Cancel a running session. Sends a cancel signal via NATS and stops the Skiff pod.

**Response (200):**

```json
{
  "status": "cancelled",
  "session": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Session cancelled |
| 400  | Session not found or not running |

**curl example:**

```bash
curl -X DELETE http://localhost:8080/api/v1/sessions/f47ac10b-58cc-4372-a567-0e02b2c3d479 \
  -H "Authorization: Bearer $TOKEN"
```

### GET /api/v1/sessions/{id}/transcript

Retrieve the transcript for a session.

When called with `Accept: text/event-stream`, this endpoint streams live transcript events via SSE (Server-Sent Events) until the session completes or the client disconnects. Otherwise, it returns the full transcript as a static JSON response.

**Static response (200):**

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

**SSE stream:** each event is a `data:` line containing a JSON transcript event. When the session reaches a terminal state (`completed`, `error`, `timeout`, `cancelled`), a `done` event is sent:

```
data: {"type":"assistant","content":"Reading file...","ts":"2026-03-25T14:30:05Z"}

data: {"type":"tool","tool":"Read","input":{"file_path":"/src/main.go"},"ts":"2026-03-25T14:30:10Z"}

event: done
data: {"status":"completed"}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Transcript returned (or SSE stream started) |
| 404  | Session or transcript not found |

**curl examples:**

```bash
# Static fetch
curl http://localhost:8080/api/v1/sessions/$SESSION_ID/transcript \
  -H "Authorization: Bearer $TOKEN"

# Live SSE stream
curl -N http://localhost:8080/api/v1/sessions/$SESSION_ID/transcript \
  -H "Authorization: Bearer $TOKEN" \
  -H "Accept: text/event-stream"
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
| `provider`   | string | yes      | Provider type: `anthropic`, `google-vertex`, `github`, or `gitlab` |
| `auth_type`  | string | yes      | One of: `api_key`, `service_account`, `adc`, `pat` |
| `credential` | string | yes      | Raw credential material (API key or JSON service account key) |
| `project_id` | string | no       | GCP project ID (Vertex only) |
| `region`     | string | no       | GCP region (Vertex only) |

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

Manage recurring tasks with cron expressions. The scheduler checks for due schedules every 60 seconds.

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

### POST /api/v1/schedules

Create a new schedule.

**Request body:**

```json
{
  "name": "nightly-tests",
  "cron": "0 2 * * *",
  "prompt": "Run the full test suite and fix any failures",
  "repo": "https://github.com/example/myproject.git",
  "provider": "anthropic",
  "scope_preset": "",
  "timeout": 3600,
  "enabled": true
}
```

| Field          | Type   | Required | Description |
|----------------|--------|----------|-------------|
| `name`         | string | yes      | Display name |
| `cron`         | string | yes      | 5-field cron expression (min hour dom month dow) |
| `prompt`       | string | yes      | Task prompt |
| `repo`         | string | no       | Git repository URL |
| `provider`     | string | no       | LLM provider name |
| `scope_preset` | string | no       | Scope preset name |
| `timeout`      | int    | no       | Task timeout in seconds |
| `enabled`      | bool   | yes      | Whether the schedule is active |

Cron syntax supports: exact values, wildcards (`*`), step values (`*/5`), ranges (`1-5`), and lists (`1,3,5`).

**Response (201):**

```json
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
  "next_run": "2026-03-26T02:00:00Z",
  "created_at": "2026-03-25T14:00:00Z"
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Schedule created |
| 400  | Invalid body or cron expression |
| 500  | Storage error |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/schedules \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "weekly-deps",
    "cron": "0 9 * * 1",
    "prompt": "Update all Go dependencies and run tests",
    "repo": "https://github.com/example/myproject.git",
    "enabled": true
  }'
```

### GET /api/v1/schedules/{id}

Get a single schedule.

**Response (200):** same shape as a single item in the list response.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Schedule found |
| 404  | Schedule not found |

### PUT /api/v1/schedules/{id}

Update a schedule. The cron expression is re-validated and `next_run` is recomputed.

**Request body:** same fields as POST (the `id` in the URL takes precedence).

**Response (200):** the updated schedule object.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Schedule updated |
| 400  | Invalid body or cron expression |
| 500  | Storage error |

### DELETE /api/v1/schedules/{id}

Delete a schedule.

**Response (200):**

```json
{
  "deleted": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Schedule deleted |
| 500  | Storage error |

### POST /api/v1/schedules/{id}/enable

Enable or disable a schedule. When enabling, `next_run` is recomputed from the current time.

**Request body:**

```json
{
  "enabled": true
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
| 200  | Schedule updated |
| 400  | Invalid body |
| 405  | Method not allowed (must be POST) |
| 500  | Storage error |

**curl example:**

```bash
# Disable a schedule
curl -X POST http://localhost:8080/api/v1/schedules/$SCHEDULE_ID/enable \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled": false}'
```

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

Manage the MCP tool registry. Builtin tools (`github`, `gitlab`) are read-only and cannot be modified or deleted.

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

**curl example:**

```bash
curl http://localhost:8080/api/v1/tools \
  -H "Authorization: Bearer $TOKEN"
```

### POST /api/v1/tools

Register a new custom tool.

**Request body:**

```json
{
  "name": "my-tool",
  "display_name": "My Custom Tool",
  "tool_type": "custom",
  "mcp_command": "/usr/local/bin/my-tool-server",
  "mcp_args": ["--port", "3000"],
  "api_host": "https://my-tool.example.com",
  "auth_header": "Authorization",
  "auth_format": "Bearer %s",
  "operations": [
    { "name": "read_data", "description": "Read data from the tool", "risk": "read" },
    { "name": "write_data", "description": "Write data to the tool", "risk": "write" }
  ]
}
```

| Field          | Type   | Required | Description |
|----------------|--------|----------|-------------|
| `name`         | string | yes      | Unique tool identifier (kebab-case) |
| `display_name` | string | yes      | Human-readable name |
| `tool_type`    | string | no       | `"builtin"` or `"custom"` (defaults to `"custom"`) |
| `mcp_command`  | string | no       | MCP server command |
| `mcp_args`     | array  | no       | MCP server command arguments |
| `api_host`     | string | no       | API base URL |
| `auth_header`  | string | no       | HTTP header name for authentication |
| `auth_format`  | string | no       | Format string for auth header value (e.g. `"Bearer %s"`) |
| `operations`   | array  | no       | List of operations with `name`, `description`, and `risk` |

**Response (201):** the created tool object.

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Tool created |
| 400  | Missing `name` or `display_name` |
| 500  | Storage error |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/tools \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-tool",
    "display_name": "My Custom Tool",
    "operations": [
      { "name": "read_data", "description": "Read data", "risk": "read" }
    ]
  }'
```

### GET /api/v1/tools/{name}

Get a single tool by name.

**Response (200):** same shape as a single item in the list response.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Tool found |
| 404  | Tool not found |

**curl example:**

```bash
curl http://localhost:8080/api/v1/tools/github \
  -H "Authorization: Bearer $TOKEN"
```

### PUT /api/v1/tools/{name}

Update a custom tool. Builtin tools cannot be modified (returns 403).

**Request body:** same fields as POST (the `name` in the URL takes precedence).

**Response (200):** the updated tool object.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Tool updated |
| 400  | Invalid body |
| 403  | Builtin tools cannot be modified |
| 404  | Tool not found |

### DELETE /api/v1/tools/{name}

Delete a custom tool. Builtin tools cannot be deleted (returns 403).

**Response (200):**

```json
{
  "deleted": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Tool deleted |
| 403  | Builtin tools cannot be deleted |
| 404  | Tool not found |

**curl example:**

```bash
curl -X DELETE http://localhost:8080/api/v1/tools/my-tool \
  -H "Authorization: Bearer $TOKEN"
```

---

## Security Profiles

Manage security profiles that define per-tool access rules for tasks. Builtin profiles are read-only and cannot be modified or deleted.

### GET /api/v1/profiles

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
      "tools": {
        "github": {
          "rules": [
            { "repos": ["*"], "operations": ["clone", "read_prs", "read_issues", "read_contents"] }
          ]
        }
      },
      "is_builtin": false,
      "created_at": "2026-03-20T10:00:00Z",
      "updated_at": "2026-03-20T10:00:00Z"
    }
  ],
  "count": 1
}
```

**curl example:**

```bash
curl http://localhost:8080/api/v1/profiles \
  -H "Authorization: Bearer $TOKEN"
```

### POST /api/v1/profiles

Create a new security profile.

**Request body:**

```json
{
  "name": "pr-creator",
  "display_name": "PR Creator",
  "description": "Can read any repo and create PRs on specific repos",
  "tools": {
    "github": {
      "rules": [
        { "repos": ["*"], "operations": ["clone", "read_prs", "read_contents"] },
        { "repos": ["example/myproject"], "operations": ["clone", "push_branch", "create_pr_draft"] }
      ]
    }
  }
}
```

| Field          | Type   | Required | Description |
|----------------|--------|----------|-------------|
| `name`         | string | yes      | Unique profile identifier (kebab-case) |
| `display_name` | string | no       | Human-readable name |
| `description`  | string | no       | Profile description |
| `tools`        | object | yes      | Map of tool name to `ProfileToolConfig` |

Each `ProfileToolConfig` contains a `rules` array. Each rule specifies:

| Field        | Type     | Description |
|--------------|----------|-------------|
| `repos`      | string[] | Repository patterns (e.g. `["org/repo"]` or `["*"]` for all) |
| `operations` | string[] | Allowed operations for those repos |

**Response (201):** the created profile object.

**Status codes:**

| Code | Meaning |
|------|---------|
| 201  | Profile created |
| 400  | Missing `name` or `tools` |
| 500  | Storage error |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/profiles \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "read-only-github",
    "display_name": "Read-Only GitHub",
    "tools": {
      "github": {
        "rules": [
          { "repos": ["*"], "operations": ["clone", "read_prs", "read_contents"] }
        ]
      }
    }
  }'
```

### GET /api/v1/profiles/{name}

Get a single profile by name.

**Response (200):** same shape as a single item in the list response.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Profile found |
| 404  | Profile not found |

### PUT /api/v1/profiles/{name}

Update a custom profile. Builtin profiles cannot be modified (returns 403).

**Request body:** same fields as POST (the `name` in the URL takes precedence).

**Response (200):** the updated profile object.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Profile updated |
| 400  | Invalid body |
| 403  | Builtin profiles cannot be modified |
| 404  | Profile not found |

### DELETE /api/v1/profiles/{name}

Delete a custom profile. Builtin profiles cannot be deleted (returns 403).

**Response (200):**

```json
{
  "deleted": true
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Profile deleted |
| 403  | Builtin profiles cannot be deleted |
| 404  | Profile not found |

**curl example:**

```bash
curl -X DELETE http://localhost:8080/api/v1/profiles/my-profile \
  -H "Authorization: Bearer $TOKEN"
```

### POST /api/v1/profiles/build

Use AI to generate a security profile from a natural language description. Requires a system LLM to be configured (see Admin Settings).

**Request body:**

```json
{
  "description": "Read any GitHub repo and create draft PRs on example/myproject"
}
```

**Response (200):**

```json
{
  "profile": {
    "name": "github-pr-creator",
    "display_name": "GitHub PR Creator",
    "description": "Read any GitHub repo and create draft PRs on example/myproject",
    "tools": {
      "github": {
        "rules": [
          { "repos": ["*"], "operations": ["clone", "read_prs", "read_contents"] },
          { "repos": ["example/myproject"], "operations": ["clone", "push_branch", "create_pr_draft"] }
        ]
      }
    }
  }
}
```

The returned profile is not saved automatically. Submit it to `POST /api/v1/profiles` to persist it.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Profile generated |
| 400  | Missing `description` |
| 500  | LLM error |
| 503  | System LLM not configured |

**curl example:**

```bash
curl -X POST http://localhost:8080/api/v1/profiles/build \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description": "Read any GitHub repo and create draft PRs on example/myproject"}'
```

---

## Skill Repos

Configure git repositories containing Claude Code plugins (skills and agents) that are loaded into every Skiff container via `--plugin-dir`.

### GET /api/v1/admin/settings/skill-repos

Get the system-wide skill repos (admin only).

**Response (200):**

```json
{
  "repos": [
    {
      "url": "https://github.com/org/my-skills.git",
      "ref": "main",
      "name": "My Skills"
    }
  ]
}
```

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Repos returned (empty list if none configured) |
| 403  | Admin access required |

### PUT /api/v1/admin/settings/skill-repos

Set the system-wide skill repos (admin only). Replaces the entire list.

**Request body:**

```json
{
  "repos": [
    {
      "url": "https://github.com/org/my-skills.git",
      "ref": "main",
      "name": "My Skills"
    }
  ]
}
```

| Field  | Type   | Required | Description |
|--------|--------|----------|-------------|
| `url`  | string | yes      | Git repository URL |
| `ref`  | string | no       | Branch, tag, or commit (default: main) |
| `name` | string | no       | Display name |

**Response (200):** the saved repos list.

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Repos saved |
| 400  | Invalid request body |
| 403  | Admin access required |
| 500  | Storage error |

**curl example:**

```bash
curl -X PUT http://localhost:8080/api/v1/admin/settings/skill-repos \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos": [{"url": "https://github.com/org/my-skills.git", "ref": "main"}]}'
```

### GET /api/v1/user/settings/skill-repos

Get the current user's personal skill repos.

**Response (200):** same shape as the admin endpoint.

### PUT /api/v1/user/settings/skill-repos

Set the current user's personal skill repos. Replaces the entire list.

**Request body:** same shape as the admin endpoint.

**Response (200):** the saved repos list.

Both system-wide and per-user skill repos are merged at dispatch time and passed to Skiff via the `ALCOVE_SKILL_REPOS` environment variable.

---

## Task Repos

Configure git repositories containing YAML task definitions (`.alcove/tasks/*.yml`). Task repos are synced automatically every 5 minutes.

### GET /api/v1/admin/settings/task-repos

Get the system-wide task repos (admin only).

**Response (200):**

```json
{
  "repos": [
    {
      "url": "https://github.com/org/task-definitions.git",
      "ref": "main",
      "name": "Org Tasks"
    }
  ]
}
```

### PUT /api/v1/admin/settings/task-repos

Set the system-wide task repos (admin only). Replaces the entire list.

**Request/Response:** same shape as skill repos.

### GET /api/v1/user/settings/task-repos

Get the current user's personal task repos.

### PUT /api/v1/user/settings/task-repos

Set the current user's personal task repos.

---

## Task Definitions

Task definitions are YAML files discovered from registered task repos. They define reusable, parameterized tasks.

### GET /api/v1/task-definitions

List all task definitions from synced task repos.

**Response (200):**

```json
{
  "definitions": [
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
      "source_file": ".alcove/tasks/run-tests.yml"
    }
  ]
}
```

### GET /api/v1/task-definitions/{name}

Get a single task definition by name.

### POST /api/v1/task-definitions/{name}/run

Run a task definition immediately as a new task. Returns the created session.

**Response (201):** same shape as `POST /api/v1/tasks`.

### POST /api/v1/task-definitions/sync

Trigger an immediate sync of all task repos (normally happens every 5 minutes).

**Response (200):**

```json
{
  "synced": true
}
```

---

## Task Templates

Starter templates for creating task definitions.

### GET /api/v1/task-templates

List available starter templates.

**Response (200):**

```json
{
  "templates": [
    {
      "name": "basic-task",
      "description": "A simple task with a prompt and repo",
      "yaml": "name: my-task\nprompt: |\n  Your prompt here\nrepo: https://github.com/org/repo.git\ntimeout: 1800\n"
    }
  ]
}
```

---

## Admin Settings

Admin-only endpoints for system configuration. Requires the `X-Alcove-Admin: true` header (set by auth middleware for admin users).

### GET /api/v1/admin/settings/llm

Get the effective system LLM configuration. Returns the resolved configuration with source tracking for each field (`env`, `database`, or `default`).

**Response (200):**

```json
{
  "provider": "anthropic",
  "provider_source": "env",
  "model": "claude-sonnet-4-20250514",
  "model_source": "database",
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

Update the system LLM configuration. Optionally include credential material to store a system LLM credential.

**Request body:**

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-20250514",
  "credential": "sk-ant-...",
  "auth_type": "api_key"
}
```

| Field           | Type   | Required | Description |
|-----------------|--------|----------|-------------|
| `provider`      | string | no       | LLM provider: `anthropic` or `google-vertex` |
| `model`         | string | no       | Model name |
| `region`        | string | no       | GCP region (Vertex only) |
| `project_id`    | string | no       | GCP project ID (Vertex only) |
| `credential`    | string | no       | Raw credential material (stored encrypted) |
| `auth_type`     | string | no       | Required with `credential`: `api_key` or `service_account` |

**Response (200):** the effective LLM configuration (same shape as GET).

**Status codes:**

| Code | Meaning |
|------|---------|
| 200  | Settings updated |
| 400  | Invalid request body |
| 403  | Admin access required |
| 500  | Storage error |

**curl example:**

```bash
curl -X PUT http://localhost:8080/api/v1/admin/settings/llm \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "anthropic",
    "model": "claude-sonnet-4-20250514",
    "credential": "sk-ant-...",
    "auth_type": "api_key"
  }'
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
