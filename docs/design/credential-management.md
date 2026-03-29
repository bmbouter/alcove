# Credential Management Design

## Decision: Bridge Pre-fetches Tokens, Gate Stays Simple (Approach A)

Date: 2026-03-25

## Problem

Alcove needs to support multiple LLM authentication methods:

1. **Anthropic API key** — static key, injected as `x-api-key` header
2. **Vertex AI with service account JSON** — OAuth2 token from SA file, refreshed hourly
3. **Vertex AI with ADC (Application Default Credentials)** — OAuth2 token from
   `gcloud auth application-default login`, refreshed hourly

The current implementation passes a single static key (`GATE_LLM_API_KEY`) from
Bridge to Gate. This works for Anthropic API keys but not for Vertex AI, which
requires OAuth2 token acquisition and periodic refresh.

In production (OpenShift), multiple users will have different credentials. File
mounts (SA JSON, ADC) are not viable per-task. Credentials must flow through
the system programmatically.

## Architecture

```
User registers credentials
        │
        ▼
┌──────────────┐
│   Bridge     │  Stores credentials (encrypted in PostgreSQL)
│              │  Pre-fetches OAuth2 tokens at task dispatch time
│              │  Provides token refresh endpoint
└──────┬───────┘
       │ passes bearer token (not raw credentials)
       ▼
┌──────────────┐
│   Gate       │  Receives a bearer token or API key
│   (sidecar)  │  Injects into LLM request headers
│              │  Calls Bridge for token refresh if needed
└──────────────┘
```

### Key Principles

1. **Raw credentials never enter Gate or Skiff containers.** Bridge is the only
   component that holds SA JSON files, API keys, or ADC tokens. Gate receives
   only short-lived bearer tokens.

2. **Gate stays simple.** Gate does not know about OAuth2 flows, Google auth
   libraries, or credential types. It receives a token string and a provider
   type, and injects the appropriate header.

3. **Token refresh via Bridge API.** For tasks that outlast a token's lifetime
   (>1 hour), Gate calls Bridge's token refresh endpoint using its session token
   for authentication.

4. **Multi-user ready.** Credentials are stored per-provider in PostgreSQL,
   associated with the user or team that registered them. At dispatch time,
   Bridge resolves which credential to use based on the task's provider and
   submitter.

## Credential Storage

### Database Schema

```sql
CREATE TABLE IF NOT EXISTS provider_credentials (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL,              -- human-readable label
    provider    TEXT NOT NULL,              -- "anthropic" or "google-vertex"
    auth_type   TEXT NOT NULL,              -- "api_key", "service_account", "adc"
    credential  BYTEA NOT NULL,            -- encrypted credential material
    project_id  TEXT,                       -- Vertex AI project ID (for google-vertex)
    region      TEXT DEFAULT 'us-east5',   -- Vertex AI region
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Credential Material by Type

| Auth Type | What's Stored (encrypted) | What Bridge Produces |
|-----------|--------------------------|---------------------|
| `api_key` | The raw API key string | The same API key (no transformation) |
| `service_account` | Full SA JSON file contents | OAuth2 access token (1 hour TTL) |
| `adc` | ADC JSON from `~/.config/gcloud/application_default_credentials.json` | OAuth2 access token (1 hour TTL) |

### Encryption

Credentials are encrypted at rest using AES-256-GCM. The encryption key is
derived from a master key configured via `ALCOVE_CREDENTIAL_KEY` environment
variable (or k8s Secret in production). Bridge decrypts credentials only at
token acquisition time, in memory.

## Token Flow

### At Task Dispatch (Bridge)

```
1. Resolve provider for the task (from request or default)
2. Look up credential for that provider in provider_credentials table
3. Decrypt the credential material
4. Based on auth_type:
   a. api_key:         token = the raw API key
   b. service_account: token = google.DefaultTokenSource(ctx, scopes).Token()
   c. adc:             token = google.DefaultTokenSource(ctx, scopes).Token()
5. Pass to Gate via env vars:
   - GATE_LLM_TOKEN = the bearer token or API key
   - GATE_LLM_PROVIDER = "anthropic" or "google-vertex"
   - GATE_LLM_TOKEN_TYPE = "api_key" or "bearer"
   - GATE_TOKEN_REFRESH_URL = http://alcove-bridge:8080/api/v1/internal/token-refresh
   - GATE_TOKEN_REFRESH_SECRET = <session UUID token>
   - GATE_VERTEX_REGION = <region from credential, default us-east5>
   - GATE_VERTEX_PROJECT = <GCP project ID from credential>
```

### In Gate (Header Injection)

```
When proxying an LLM request:
  if token_type == "api_key":
      set header "x-api-key: <token>"
  if token_type == "bearer":
      set header "Authorization: Bearer <token>"
```

### Token Refresh (for tasks > 1 hour)

```
Gate detects a 401 from the upstream LLM provider
  → POST to GATE_TOKEN_REFRESH_URL with session ID + refresh secret
  → Bridge looks up the credential, acquires a fresh token
  → Returns {"token": "new-token", "expires_in": 3600}
  → Gate retries the original request with the new token
```

## API Endpoints

### Credential Management (Bridge REST API)

```
POST   /api/v1/credentials          — register a new credential
GET    /api/v1/credentials          — list credentials (names + types, NOT secrets)
GET    /api/v1/credentials/{id}     — get credential metadata
DELETE /api/v1/credentials/{id}     — delete a credential
```

### Token Refresh (Internal, Gate → Bridge)

```
POST   /api/v1/internal/token-refresh
  Body: {"session_id": "...", "refresh_secret": "..."}
  Response: {"token": "...", "token_type": "bearer", "expires_in": 3600}
```

This endpoint is authenticated via the session UUID token, not the user's
auth token. The token refresh endpoint does not validate the secret (this
is a Phase 1 simplification). It is only callable by Gate sidecars for
active sessions.

## Dashboard Integration

The dashboard credential management page:

1. **Add Credential** form with:
   - Name (label)
   - Provider dropdown: Anthropic / Google Vertex AI
   - Auth type (depends on provider):
     - Anthropic: API Key (text input)
     - Vertex AI: Service Account JSON (file upload) or ADC JSON (file upload)
   - Project ID (for Vertex AI only)
   - Region (for Vertex AI, default: us-east5)

2. **Credentials list** showing name, provider, type, created date.
   Never displays the actual credential material.

3. **New Task form** updated to select a credential (or use default).

## Phase 1 Simplification

For the initial implementation:

- Single-user credential store (no per-user association yet)
- Credentials registered via API (dashboard credential page can come later)
- Encryption key from environment variable (`ALCOVE_CREDENTIAL_KEY`)
- Token refresh on 401 detection (simple retry)
- No credential rotation or expiry notifications

Multi-user credential association, RBAC, and audit logging are Phase 2+.

## Files Affected

### New Files
- `internal/bridge/credentials.go` — credential CRUD, encryption, token acquisition
- `internal/bridge/credentials_test.go` — tests for encryption + token acquisition

### Modified Files
- `internal/bridge/api.go` — add credential management + token refresh endpoints
- `internal/bridge/dispatcher.go` — resolve credentials at dispatch, pass tokens to Gate
- `internal/bridge/config.go` — add `CredentialKey` config field
- `internal/gate/proxy.go` — use `GATE_LLM_TOKEN_TYPE` for header selection, add 401 retry with refresh
- `cmd/gate/main.go` — read new env vars
- `cmd/bridge/main.go` — add provider_credentials table to schema
- `go.mod` — add `golang.org/x/oauth2` dependency

## Gate Vertex API Translation

Gate does more than simple header injection for Vertex AI. When the provider is
`google-vertex`, Gate translates Anthropic-format requests to Vertex AI format:

- **URL rewriting** — rewrites the request URL to Vertex AI `rawPredict` or
  `streamRawPredict` endpoints
- **Model name conversion** — converts model names from Anthropic format (using
  `-` separators) to Vertex format (using `@`)
- **Body field stripping** — removes `model` and `context_management` fields
  from the request body
- **Header stripping** — removes `anthropic-beta`, `anthropic-version`, and
  `x-api-key` headers
- **Body field injection** — adds `anthropic_version` to the request body

This translation happens transparently so that Skiff (running Claude Code) can
use the standard Anthropic SDK format regardless of the upstream LLM provider.

## Alternatives Considered

### Approach B: Gate Handles All Auth Flows
Gate receives raw credential material and does OAuth2 internally. Rejected
because it puts raw credentials (SA JSON, API keys) into ephemeral containers,
increasing blast radius if a Skiff pod is compromised via prompt injection.

### Approach C: Dedicated Credential Service
Separate vault-like service for credential management. Rejected as overkill
for Phase 1. Can be added later (the Bridge credential API is a natural
migration path to an external vault).
