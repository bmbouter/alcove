# Database Schema Reference

Alcove uses PostgreSQL (referred to as "Ledger") to store session data,
credentials, user accounts, and schedules. This document describes every table,
its columns, and the migration system that manages the schema.

## Tables Overview

| Table | Purpose |
|-------|---------|
| `sessions` | Records of Skiff session executions (prompts, transcripts, outcomes) |
| `provider_credentials` | Encrypted LLM provider credentials (API keys, service account tokens) |
| `auth_users` | Local user accounts for Bridge authentication |
| `auth_sessions` | Active login sessions (bearer tokens) |
| `personal_api_tokens` | Personal API tokens for CLI/API authentication (postgres auth only) |
| `schedules` | Recurring agent definitions (cron-based) |
| `schema_migrations` | Tracks which migrations have been applied (auto-created) |

## sessions

Stores one row per Skiff session execution. This is the primary audit trail for
all agent activity.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | `UUID` | `PRIMARY KEY` | Session identifier, generated at dispatch time |
| `task_id` | `UUID` | `NOT NULL` | Logical task ID (may differ from session ID for retries) |
| `submitter` | `TEXT` | `NOT NULL` | Username of the person or system that started the session |
| `prompt` | `TEXT` | `NOT NULL` | The natural-language prompt sent to the agent |
| `scope` | `JSONB` | `NOT NULL DEFAULT '{}'` | Scope definition controlling what services/operations the session may access |
| `provider` | `TEXT` | `NOT NULL` | LLM provider used (e.g., `anthropic`, `google-vertex`) |
| `started_at` | `TIMESTAMPTZ` | `NOT NULL` | When the Skiff pod was created |
| `finished_at` | `TIMESTAMPTZ` | nullable | When the session completed (null while running) |
| `exit_code` | `INT` | nullable | Process exit code from Claude Code (null while running) |
| `outcome` | `TEXT` | nullable | Final status: `completed`, `error`, `timeout`, `cancelled` |
| `transcript` | `JSONB` | nullable | Array of transcript events appended during execution |
| `proxy_log` | `JSONB` | nullable | Array of Gate proxy log entries (method, URL, decision) |
| `artifacts` | `JSONB` | nullable | Array of artifact descriptors produced by the session |
| `parent_id` | `UUID` | `REFERENCES sessions(id)` | Links to a parent session for chained/retry tasks |

**Note:** The `sessions` table has no `repo` column. The `Session` Go struct has
a `Repo` field, but it is not persisted to the database. The `repo` query
parameter in the sessions API actually searches the `prompt` column.

The `transcript` and `proxy_log` columns use atomic JSONB append operations
(`COALESCE(col, '[]'::jsonb) || $1::jsonb`) so that concurrent appends do not
require read-modify-write cycles.

## provider_credentials

Stores LLM provider credentials. The `credential` column holds the raw secret
(API key or service account JSON) as encrypted bytes.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | `UUID` | `PRIMARY KEY` | Credential identifier |
| `name` | `TEXT` | `NOT NULL` | Human-readable name for this credential |
| `provider` | `TEXT` | `NOT NULL` | Provider name (e.g., `anthropic`, `google-vertex`) |
| `auth_type` | `TEXT` | `NOT NULL` | Authentication method (e.g., `api_key`, `service_account`) |
| `credential` | `BYTEA` | `NOT NULL` | The secret material (API key, OAuth token, or service account JSON) |
| `project_id` | `TEXT` | nullable | Cloud project ID (used for Google Vertex AI) |
| `region` | `TEXT` | `DEFAULT 'us-east5'` | Cloud region for the provider endpoint |
| `created_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | When the credential was stored |
| `updated_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | When the credential was last modified |

## auth_users

Local user accounts for Bridge dashboard and API authentication. Passwords are
stored as argon2id hashes.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `username` | `TEXT` | `PRIMARY KEY` | Unique username |
| `password` | `TEXT` | `NOT NULL` | Argon2id password hash (format: `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>`) |
| `created_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | Account creation time |
| `updated_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | Last password change time |

## auth_sessions

Active login sessions. Each row represents a valid bearer token that can be
used to authenticate API requests.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `token` | `TEXT` | `PRIMARY KEY` | Opaque bearer token (hex-encoded random bytes) |
| `username` | `TEXT` | `NOT NULL, REFERENCES auth_users(username) ON DELETE CASCADE` | The authenticated user |
| `expires_at` | `TIMESTAMPTZ` | `NOT NULL` | When this token expires |
| `created_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | When the token was issued |

### Indexes

| Index | Column(s) | Notes |
|-------|-----------|-------|
| `idx_auth_sessions_expires` | `expires_at` | Speeds up expired token cleanup queries |

When a user is deleted from `auth_users`, all their sessions are automatically
removed via `ON DELETE CASCADE`.

## personal_api_tokens

Personal API tokens for CLI and API authentication. Only used with the postgres auth backend.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | `TEXT` | `PRIMARY KEY` | Token UUID |
| `username` | `TEXT` | `NOT NULL, REFERENCES auth_users(username) ON DELETE CASCADE` | Token owner |
| `name` | `TEXT` | `NOT NULL DEFAULT ''` | User-provided description (e.g., "laptop CLI") |
| `token_hash` | `TEXT` | `NOT NULL` | bcrypt hash of the actual token |
| `created_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | When the token was created |
| `last_accessed_at` | `TIMESTAMPTZ` | nullable | When this token was last used (updated asynchronously) |

### Indexes

| Index | Column(s) | Notes |
|-------|-----------|-------|
| `idx_personal_api_tokens_username` | `username` | Speeds up token lookup by user |

**Token Format:** Personal API tokens use the format `apat_` followed by 40 hex characters.

**Security:** The actual token is only shown once at creation time. The stored `token_hash` is compared using bcrypt when validating tokens.

**Authentication:** Tokens work via Basic Auth where the username is the account username and the "password" is the personal API token.

When a user is deleted from `auth_users`, all their personal API tokens are automatically
removed via `ON DELETE CASCADE`.

## schedules

Defines recurring sessions that Bridge's scheduler executes on a cron schedule.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | `UUID` | `PRIMARY KEY` | Schedule identifier |
| `name` | `TEXT` | `NOT NULL` | Human-readable schedule name |
| `cron` | `TEXT` | `NOT NULL` | Cron expression (e.g., `0 */6 * * *` for every 6 hours) |
| `prompt` | `TEXT` | `NOT NULL` | The prompt to send to the agent on each run |
| `repo` | `TEXT` | nullable | Git repository URL to clone for the session |
| `provider` | `TEXT` | nullable | LLM provider override (uses default if null) |
| `scope_preset` | `TEXT` | nullable | Named scope preset to apply to scheduled runs |
| `timeout` | `INT` | `DEFAULT 3600` | Maximum session duration in seconds |
| `enabled` | `BOOLEAN` | `DEFAULT true` | Whether the schedule is active |
| `last_run` | `TIMESTAMPTZ` | nullable | When the schedule last triggered a session |
| `next_run` | `TIMESTAMPTZ` | nullable | Computed next trigger time |
| `created_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | When the schedule was created |

## schema_migrations

Auto-created by the migration runner on first startup. Tracks which migration
versions have been applied to this database.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `version` | `INT` | `PRIMARY KEY` | Migration version number (parsed from filename prefix) |
| `applied_at` | `TIMESTAMPTZ` | `NOT NULL DEFAULT NOW()` | When this migration was applied |

This table is not defined in any migration file. It is created directly by the
`Migrate` function in `internal/bridge/migrate.go` using
`CREATE TABLE IF NOT EXISTS`.

## Migration Runner

The migration system is a custom, lightweight runner implemented in
`internal/bridge/migrate.go`. It runs automatically every time Bridge starts.

### How it works

1. **Advisory lock.** The runner acquires a PostgreSQL advisory lock
   (`pg_advisory_lock`) with a fixed lock ID to prevent multiple Bridge
   instances from running migrations concurrently during rolling deployments.

2. **Ensure tracking table.** It creates the `schema_migrations` table if it
   does not already exist.

3. **Discover applied versions.** It reads all rows from `schema_migrations`
   into a map.

4. **Read migration files.** Migration SQL files are embedded into the Bridge
   binary at compile time using `//go:embed migrations/*.sql`. The runner reads
   the embedded filesystem and sorts files lexicographically (which, given the
   zero-padded numeric prefix, produces the correct order).

5. **Apply pending migrations.** For each file whose version number is not in
   the applied map:
   - Parse the version from the filename (e.g., `001_initial_schema.sql` becomes version 1)
   - Begin a database transaction
   - Execute the SQL
   - Insert a row into `schema_migrations`
   - Commit the transaction
   - If any step fails, the transaction rolls back and Bridge exits with an error

6. **Release the advisory lock.** The lock is released via `defer`.

### How to add a migration

Create a new `.sql` file in `internal/bridge/migrations/` with the next
sequential number:

```
internal/bridge/migrations/
  001_initial_schema.sql    (existing)
  002_add_task_labels.sql   (new)
```

The file is automatically included in the next build. See the
[Development Guide](development-guide.md#adding-a-database-migration) for
detailed instructions and conventions.

### File naming

```
NNN_short_description.sql
```

The version number is extracted by splitting the filename on `_` and parsing
the first segment as an integer. Both `001` and `1` resolve to version 1, but
zero-padded numbers are preferred for consistent sorting.
