# Auth Backend Design

Date: 2026-03-26

## Decision: Three Auth Backends with Explicit Selection

`AUTH_BACKEND=memory` (default), `AUTH_BACKEND=postgres`, or
`AUTH_BACKEND=rh-identity`. No auto-detection.

## Interfaces

```go
type Authenticator interface {
    Authenticate(username, password string) (string, error)
    ValidateToken(token string) (string, bool)
    InvalidateToken(token string)
}

type UserManager interface {
    CreateUser(ctx context.Context, username, password string) error
    DeleteUser(ctx context.Context, username string) error
    ListUsers(ctx context.Context) ([]UserInfo, error)
    ChangePassword(ctx context.Context, username, newPassword string) error
}

type UserInfo struct {
    Username  string    `json:"username"`
    CreatedAt time.Time `json:"created_at"`
}
```

## Backends

- **MemoryStore**: current UserStore renamed. Implements Authenticator only.
- **PgStore**: PostgreSQL-backed. Implements Authenticator + UserManager.
- **RHIdentityStore**: Red Hat identity header-based. Implements Authenticator + UserManager.

### RHIdentityStore

Trusts the `X-RH-Identity` header set by Red Hat's Turnpike gateway. The
header contains a base64-encoded JSON identity, supporting two types:

**SAML Associate Identity (SSO users):**
```json
{
  "identity": {
    "type": "Associate",
    "auth_type": "saml-auth",
    "associate": {
      "givenName": "Alice",
      "surname": "Smith",
      "rhatUUID": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "email": "alice@redhat.com",
      "Role": ["user"]
    }
  }
}
```

**TBR User Identity (token-based registry):**
```json
{
  "identity": {
    "type": "User",
    "auth_type": "basic-auth",
    "org_id": "12345",
    "user": {
      "username": "alice"
    }
  }
}
```

#### TBR Identity Association

Token Based Registry (TBR) identities can be associated with existing SSO users:

1. **Association Management**: SSO users can associate TBR identities through the dashboard
2. **Identity Resolution**: When TBR identity arrives in X-RH-Identity, Bridge resolves it to the associated SSO user
3. **Unique Associations**: Each TBR identity can only be associated with one SSO user
4. **API Authentication**: Enables API calls using TBR tokens while maintaining user identity

Key behaviors:

- **JIT user provisioning:** On first SAML request, a user record is created in the
  `auth_users` table using the identity fields (username from email, external_id
  from rhatUUID, display_name from givenName + surname). No password is stored.
- **TBR Resolution:** TBR identities are resolved to SSO users via association table lookup.
- **No login form:** Authentication is handled entirely by Turnpike. Bridge
  does not render a login form or accept password-based login.
- **No session tokens:** Each request is authenticated by the header. Bridge
  does not issue or validate session tokens.
- **Admin bootstrap:** Initial admins are configured via `rh_identity_admins`
  in `alcove.yaml` or `RH_IDENTITY_ADMINS` env var (comma-separated usernames).
  When a user matching this list is provisioned, they receive the admin flag.
- **Admin management:** After bootstrap, existing admins can promote or demote
  users from the dashboard (same UI as other backends).

#### TBR Association Schema

```sql
CREATE TABLE IF NOT EXISTS tbr_identity_associations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id TEXT NOT NULL REFERENCES auth_users(username) ON DELETE CASCADE,
    tbr_org_id TEXT NOT NULL,
    tbr_username TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Unique TBR identity constraint
CREATE UNIQUE INDEX idx_tbr_identity_unique
    ON tbr_identity_associations(tbr_org_id, tbr_username);
```

#### TBR Association API

```
GET    /api/v1/auth/tbr-associations     — list current user's associations
POST   /api/v1/auth/tbr-associations     — create new association
DELETE /api/v1/auth/tbr-associations/{id} — remove association (user must own it)
```

## Schema

```sql
CREATE TABLE IF NOT EXISTS auth_users (
    username   TEXT PRIMARY KEY,
    password   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS auth_sessions (
    token      TEXT PRIMARY KEY,
    username   TEXT NOT NULL REFERENCES auth_users(username) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

## API (postgres backend only)

```
GET    /api/v1/users              — list users
POST   /api/v1/users              — create user
DELETE /api/v1/users/{username}   — delete user
PUT    /api/v1/users/{username}/password — change password
```

## Files

- `internal/auth/auth.go` — interfaces, LoginHandler, AuthMiddleware, shared utils
- `internal/auth/memory.go` — MemoryStore (current code moved)
- `internal/auth/postgres.go` — PgStore
- `internal/auth/rh_identity.go` — RHIdentityStore (X-RH-Identity header auth)
- `internal/auth/users_api.go` — user CRUD HTTP handlers
- `internal/bridge/config.go` — AuthBackend field
- `cmd/bridge/main.go` — factory, schema, route registration
