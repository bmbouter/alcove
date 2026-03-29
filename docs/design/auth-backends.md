# Auth Backend Design

Date: 2026-03-26

## Decision: Dual Auth Backends with Explicit Selection

`AUTH_BACKEND=memory` (default) or `AUTH_BACKEND=postgres`. No auto-detection.

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
- `internal/auth/users_api.go` — user CRUD HTTP handlers
- `internal/bridge/config.go` — AuthBackend field
- `cmd/bridge/main.go` — factory, schema, route registration
