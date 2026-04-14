# Teams: Shared Resource Ownership for Alcove

## Problem

Today every resource in Alcove (sessions, credentials, security profiles, agent
definitions, schedules, workflows, tools, agent repos) is owned by a single user.
There is no way for a group of users to share a common set of resources. Teams
working together must each configure their own credentials, agent definitions,
and schedules independently, with no shared visibility into each other's sessions
or workflows.

## Solution

Introduce teams as the universal ownership unit. Every resource belongs to a team.
Every user belongs to one or more teams. A personal team is auto-created for each
user on signup, preserving the current single-user experience. Users can create
additional teams and invite others, enabling shared ownership with equal
permissions for all members.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Membership model | Multi-team per user | Marginal complexity over single-team; avoids future migration |
| Personal workspace | Auto-created personal team | One ownership concept everywhere; no personal-vs-team dual path |
| Permissions within a team | Equal — all members have full access | Matches requirement; no roles/ACLs needed |
| Team management | Self-service creation, member-managed | Any user creates teams; any member invites/removes; admins can override |
| Credential sharing | Fully shared, no restrictions | Consistent with "team acts like a single user" |
| Agent repos | Team-level setting | Avoids duplicate definitions when multiple users add the same repo |
| Active team context | Explicit team switcher | Clean mental model; UI scoped to one team at a time |
| Implementation approach | Team ID replaces owner column | Clean model, uniform queries, no legacy dual-path |

## Data Model

### New Tables

```sql
CREATE TABLE teams (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    is_personal BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE team_members (
    team_id  UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    PRIMARY KEY (team_id, username)
);
CREATE INDEX idx_team_members_username ON team_members(username);
```

### New Settings Table

```sql
CREATE TABLE team_settings (
    team_id    UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, key)
);
```

### Resource Table Changes

Every resource table changes from `owner TEXT` to `team_id UUID REFERENCES teams(id)`:

| Table | Column change | Notes |
|-------|--------------|-------|
| `sessions` | Add `team_id UUID`; keep `submitter` | `submitter` is who ran it; `team_id` is ownership |
| `provider_credentials` | `owner` → `team_id UUID` | |
| `security_profiles` | `owner` → `team_id UUID` | |
| `agent_definitions` | `owner` → `team_id UUID` | |
| `schedules` | `owner` → `team_id UUID` | |
| `workflows` | `owner` → `team_id UUID` | |
| `workflow_runs` | `owner` → `team_id UUID` | |
| `mcp_tools` | `owner` → `team_id UUID` | |

Unique constraints update accordingly:
- `UNIQUE(name, owner)` → `UNIQUE(name, team_id)` on security_profiles, mcp_tools
- `UNIQUE(source_key)` on agent_definitions and workflows becomes `UNIQUE(source_key, team_id)` so multiple teams can sync the same repo independently

## API Design

### Active Team Context

The active team is sent via the `X-Alcove-Team` HTTP header. The auth middleware
validates that the authenticated user is a member of the specified team. If the
header is missing, Bridge defaults to the user's personal team.

Admins can specify any team ID, even teams they are not a member of.

### New Endpoints

```
GET    /api/v1/teams              — list teams the current user belongs to
POST   /api/v1/teams              — create a team (any authenticated user)
GET    /api/v1/teams/{id}         — get team details + member list
PUT    /api/v1/teams/{id}         — update team name
DELETE /api/v1/teams/{id}         — delete team (personal teams cannot be deleted)
POST   /api/v1/teams/{id}/members           — add a member
DELETE /api/v1/teams/{id}/members/{username} — remove a member
```

Authorization for member management: the caller must be a member of the team,
or a Bridge admin.

### Changes to Existing Endpoints

Every existing handler changes the same way:

- **List**: `WHERE owner = $username` → `WHERE team_id = $active_team`
- **Create**: set `team_id = $active_team` instead of `owner = $username`
- **Get/Delete**: verify `team_id = $active_team` instead of `owner = $username`

The `checkOwnership()` function becomes `checkTeamAccess()` — verifies the
resource's `team_id` matches the active team and that the user is a member.

### Agent Repos

Agent repos move from `user_settings` to `team_settings`. The syncer pulls
definitions into the team scope. Any team member can add/remove repos for the
team via the existing agent repos UI (now scoped to the active team).

## CLI Changes

### Team Subcommands

```
alcove teams list                           — list your teams
alcove teams create <name>                  — create a team
alcove teams use <name>                     — set active team for current profile
alcove teams use --personal                 — switch back to personal team
alcove teams add-member <team> <username>   — add a member
alcove teams remove-member <team> <username> — remove a member
alcove teams delete <team>                  — delete a team
```

### Active Team in Config

The active team is persisted per profile in the CLI config:

```yaml
profiles:
    hcmai:
        server: https://...
        username: bmbouter
        password: ...
        active_team: engineering
```

The `--team <name>` flag on any command overrides the profile default for that
invocation.

## Dashboard UI

### Team Switcher

A dropdown in the top nav bar, next to the username. Shows all teams the user
belongs to. The personal team appears first, labeled as "My Workspace." Selecting
a team reloads the current view scoped to that team.

### Team Management Page

A new "Teams" section accessible from the nav. Contains:

- List of teams the user belongs to
- "Create Team" button
- Per-team view: team name, member list, add/remove member controls
- Delete team button (disabled for personal teams)
- Agent repos configuration (moved from Account settings, scoped to active team)

### No Changes to Existing Views

Sessions, credentials, agents, schedules, workflows, security profiles, and
tools views work exactly as today. The data is scoped by the active team via
the team switcher. No new columns in table views, no team badges on rows.

## Database Migration

A single migration file performs the following steps in order:

1. Create `teams`, `team_members`, and `team_settings` tables
2. For each user in `auth_users`: create a personal team (`is_personal = true`,
   name = `"{username}'s workspace"`), insert into `team_members`
3. Add `team_id UUID` column (nullable) to all resource tables
4. Backfill `team_id` from the personal team of each resource's current
   `owner`/`submitter` value
5. Set `team_id` to `NOT NULL`, add foreign key constraint
6. Drop `owner` columns (keep `submitter` on `sessions`)
7. Recreate unique constraints with `team_id`
8. Migrate `user_settings` agent repo entries to `team_settings`

## Auth Backend Integration

### Postgres Backend

No special handling. Users are created via the API; personal teams are created
at the same time.

### RH-Identity Backend

When a new user is auto-provisioned via the `X-RH-Identity` header, Bridge
creates their personal team as part of the `UpsertUser()` flow.

## Edge Cases

### Deleting a Team

All resources owned by the team are deleted (CASCADE on `team_id` foreign key).
Running sessions are cancelled first. The UI shows a confirmation dialog with a
count of resources that will be destroyed. Personal teams cannot be deleted.

### Removing a User from a Team

The user immediately loses access to that team's resources. If their CLI config
has `active_team` set to the removed team, the next request falls back to their
personal team. Resources stay with the team.

### Deleting a User

Membership in all teams is removed (CASCADE on `team_members`). Their personal
team and all its resources are deleted. Shared teams persist — only the
membership row is removed.

### Schedule and Workflow Execution

Schedules and workflows fire in their team context, using the team's credentials
and security profiles. The resulting session's `submitter` is the system
identifier (schedule name or workflow name); the `team_id` is the owning team.

### Personal Team Constraints

Personal teams cannot be renamed, deleted, or have members added/removed. They
always have exactly one member: the user they were created for. The
`is_personal` flag on the `teams` table enforces this in the API handlers.

## Components Not Affected

- **Gate**: No changes. Gate receives session-level config (credentials, scopes)
  and does not know about teams.
- **Skiff**: No changes. Skiff runs tasks; team context is resolved by Bridge
  before dispatch.
- **Hail (NATS)**: No changes. Message topics use session IDs, not team IDs.
- **Ledger schema**: Sessions table gains `team_id`, but the Ledger HTTP client
  API is unchanged.
