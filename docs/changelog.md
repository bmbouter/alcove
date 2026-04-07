# Changelog

All notable changes to Alcove are documented here. This project uses
[Semantic Versioning](https://semver.org/).

## v0.4.11

### Bug Fixes
- Fix transcript polling fallback: clear stream state when proxy closes
  the SSE connection, allowing the 5-second refresh to fetch transcript
  data from the database.

## v0.4.10

### Bug Fixes
- Fix transcript and proxy log not appearing during running sessions.
  Skiff batched transcript events and only wrote to the database every
  50 events or at session end. The 5-second flush timer existed but was
  never used for flushing. Now flushes to the DB every 5 seconds.
- Fix client-side: don't abort active stream on polling refresh, show
  Live indicator during polling fallback.

## v0.4.9

### Improvements
- Replace EventSource with fetch()+ReadableStream for transcript streaming.
  EventSource is incompatible with the Akamai+Turnpike proxy chain.
  fetch() works through the proxy chain already, so ReadableStream extends
  it to stream responses incrementally. Falls back to 5-second polling
  automatically. Live indicator works in both modes.

## v0.4.8

### Bug Fixes
- Fix SSE live streaming on Turnpike/rh-identity: pass withCredentials
  to EventSource so SSO cookies are sent. Without cookies, Turnpike
  redirected to the login page and EventSource stayed in CONNECTING.

## v0.4.7

### Debugging
- Add SSE debug logging to browser console and server for diagnosing
  live streaming issues on staging.

## v0.4.6

### Bug Fixes
- Fix SSE live streaming on HTTP/2: remove illegal Transfer-Encoding: chunked
  header and padding hack. HTTP/2 forbids Transfer-Encoding (RFC 7540).

## v0.4.5

### Bug Fixes
- Fix SSE live streaming: increase proxy buffer padding from 4KB to 32KB.
  Server logs confirmed the SSE connection reached Bridge but data never
  reached the browser through 3scale/Turnpike.

## v0.4.4

### Bug Fixes
- Fix SSE live streaming through haproxy/3scale (Turnpike): send 4KB padding
  block to push past proxy buffer threshold and force streaming mode.

## v0.4.3

### Security
- Task prompts require `ready-for-dev` label before acting on issues.
  Autonomous dev task verifies comment author is `bmbouter`.

### Bug Fixes
- Fix SSE live streaming through reverse proxies: add `X-Accel-Buffering: no`
  header to SSE responses, disabling Turnpike/nginx/haproxy response buffering.
- Remove diagnostic proxy log logging from Gate (flush fix confirmed).

## v0.4.2

### Bug Fixes
- Fix empty proxy logs on short-lived tasks: reduced Gate flush interval from
  30s to 5s and added synchronous flush on shutdown before container exits.

## v0.4.1

### Bug Fixes
- Add diagnostic logging for Gate proxy log delivery to investigate empty
  proxy logs on OpenShift staging. Gate now logs the target URL at startup
  and includes full URL and response body on POST failures.

### Features
- Add autonomous developer task definition and alcove-developer security profile
  for fully autonomous software development lifecycle.

## v0.4.0

### Features
- **YAML security profiles**: Define security profiles in `.alcove/security-profiles/*.yml`
  alongside task definitions. Synced from task repos, read-only in UI, with profile
  validation on task definitions (sync errors block dispatch).
- **GitHub event polling**: Poll GitHub Events API every 60 seconds for event-triggered
  tasks. Works in local dev without webhooks. Supports ETag conditional requests,
  deduplication, and per-user credentials.
- **Per-user resource ownership**: All resources (task definitions, schedules, security
  profiles, sessions) are owned by real users. Removed `_system` submitter concept.
  Strict user isolation across all pages.
- **Repos page**: New top-level nav tab for managing Task Repos and Skill / Agent Repos
  (moved from dropdown menu).
- **Proxy log filtering and sorting**: Clickable column headers for sorting, dropdown
  filters for Service and Decision, summary counts.
- **Task definition cards**: Show security profiles, schedule with next/last run times,
  event triggers, and sync errors with disabled Run button.
- **PR review template**: New starter template for event-triggered PR reviews.

### UI/UX Improvements
- Renamed Profiles section to Security, Sessions to Tasks in dashboard.
- Unified Schedules page (task definitions + manual schedules in one list).
- Unified Security page (all profiles in one list, no section separators).
- Session pagination (15 per page) with relative timestamp "When" column.
- New Task tab moved to leftmost nav position, admin tabs right-aligned.
- Run Now shows "View Task" link after dispatch.
- Live indicator hidden for completed tasks.
- Renamed Skill Repos to Skill / Agent Repos.

### Backend Changes
- Removed builtin security profiles (replaced by YAML-defined profiles from repos).
- Removed system task repos (all repos are per-user).
- Added `GH_PROTOCOL=http` for gh CLI through Gate proxy.
- Migration 014: YAML profile source tracking columns.
- Migration 015: Task definition owner column with per-user source keys.
- Migration 016: GitHub poll state table for ETag and event ID tracking.

### Bug Fixes
- Fix proxy log: gateEnvVars built before URL resolution.
- Fix SyncAll cleanup for users with empty repo list.
- Fix profile validation setting empty owner on task definitions.
- Fix `notsecret` annotations on test credentials.

### CI/CD
- Added functional tests to CI (70+ API tests across 5 scripts).
- Added `go vet` to release workflow.
- Test scripts use per-user task repos endpoint.

## v0.3.10

### Bug Fixes
- Fix k8s task DNS: resolve HAIL_URL and LEDGER_URL to ClusterIPs in Bridge
  before passing to Skiff. OVN-Kubernetes blocks UDP DNS from task pods
  despite NetworkPolicy allowing port 53. Bridge resolves hostnames (where
  DNS works) and Skiff connects via IP directly.
- Remove temporary diagnostic logging from skiff-init.

## v0.3.9

### Bug Fixes
- Fix NetworkPolicy DNS: `namespaceSelector: {}` does not cover cluster DNS
  service IPs on OVN-Kubernetes/OpenShift. Changed to ports-only DNS egress
  rule (no `to` selector) which allows DNS to all destinations.
  Root cause confirmed via diagnostic logs: `net.LookupHost("alcove-hail")`
  hangs indefinitely with the namespaceSelector approach.

## v0.3.8

### Diagnostics
- Add network diagnostic logging to skiff-init: DNS resolution, raw TCP
  connection test, proxy env vars. Temporary — for debugging staging
  connectivity issues.

### Improvements
- Password security UX: confirm password on user creation and admin reset,
  8-char minimum enforced on all password endpoints, admin reset uses
  proper modal instead of prompt().

## v0.3.7

### Bug Fixes
- Fix k8s task execution: NO_PROXY was missing internal service names
  (alcove-hail, alcove-bridge, alcove-ledger), causing all Skiff traffic
  to route through Gate's HTTP proxy which rejected it with 403. NATS
  connections timed out and status updates failed.
- Disable per-task NetworkPolicy creation — static alcove-allow-internal
  provides sufficient restriction.

## v0.3.6

### Bug Fixes
- Remove alcove-default-deny NetworkPolicy — was causing DNS timeouts for Job
  pods. alcove-allow-internal already provides implicit deny via policyTypes.
- Mount Vertex AI credentials as volume (GOOGLE_APPLICATION_CREDENTIALS) instead
  of secretKeyRef, matching the pattern used by pulp-service.
- Allow cancelling stale "running" sessions after Bridge restart — sessions not
  tracked in memory now get marked as cancelled in the DB directly.

## v0.3.5

### Bug Fixes
- Fix k8s NetworkPolicy: Job pods couldn't reach Bridge or NATS because the
  per-task egress rule matched `managed-by=alcove` but Bridge/NATS use
  `part-of=alcove`. Added `part-of=alcove` label to Job pods and both
  label selectors to the egress rule.
- Add lola support: auto-detect lola modules vs Claude Code plugins in skill
  repos. Install lola in Skiff container via uv.

## v0.3.4

### Bug Fixes
- Fix rh-identity: handle users created by BootstrapAdmins (by username) that
  have no external_id. UpsertUser now falls back to username lookup and
  backfills external_id from the identity header on first login.

## v0.3.3

### Bug Fixes
- Fix rh-identity user provisioning: `ON CONFLICT (external_id)` failed because
  the `auth_users.external_id` column has a partial unique index
  (`WHERE external_id IS NOT NULL`). PostgreSQL requires the WHERE clause in the
  ON CONFLICT target to match. Error was: "no unique or exclusion constraint
  matching the ON CONFLICT specification".

## v0.3.2

### Bug Fixes
- Fix rh-identity auth: the v0.3.1 fix made `/api/v1/auth/me` fully public,
  which bypassed the rh-identity middleware so `X-Alcove-User` was never set.
  Now `/auth/me` passes through when X-RH-Identity is missing (returns
  `auth_backend` only) but processes the header normally when present.
- Frontend enters rh-identity mode based on `auth_backend` alone, no longer
  requires username from the initial probe.
- Added debug logging to rh-identity middleware for header diagnostics.

## v0.3.1

### Bug Fixes
- Fix rh-identity login: `/api/v1/auth/me` and `/api/v1/system-info` added to
  public routes so frontend can detect the auth backend without authentication.
  Previously, the middleware rejected these requests with 401 when no
  X-RH-Identity header was present (client-side fetch), causing the login form
  to show instead of auto-detecting rh-identity mode.

## v0.3.0

### JIRA/Atlassian Integration
- Gate proxies JIRA REST API via `/jira/` endpoint with full operation classification
- 18 JIRA operations with per-project scope enforcement (project key extraction
  from issue keys like PROJ-123)
- Credential form accepts JIRA Cloud credentials (email + API token, Basic auth)
- Builtin "jira" tool with security profile support
- Live-tested against Red Hat JIRA (21/21 Gate tests pass: 10 read, 9 write
  blocked, 2 cross-service isolation)

### Red Hat Identity Auth Backend
- New `auth_backend: rh-identity` for deployments behind Turnpike gateway
- Decodes `X-RH-Identity` header (Base64 JSON) for SAML-authenticated users
- JIT user provisioning from rhatUUID, email, display name
- Admin bootstrap via `rh_identity_admins` list in alcove.yaml
- No login form, no passwords, no session tokens
- 21 unit tests (identity parsing, interface compliance, middleware integration)

### System LLM Moved to Config File
- System LLM configured exclusively in `alcove.yaml` (not dashboard or API)
- Supports Anthropic (api_key) and Google Vertex AI (service_account_json)
- PUT /api/v1/admin/settings/llm returns 405 (read-only via GET)
- Dashboard shows read-only status in System Info panel

### System Info Panel
- Replaced "System LLM" admin-only modal with "System Info" panel visible
  to all users
- Shows version, runtime, auth backend, and LLM status (no secrets)
- New GET /api/v1/system-info endpoint

### Subpath Deployment Support
- Dashboard works behind reverse proxies at any subpath (e.g., /app/alcove/)
- Runtime base path detection from window.location.pathname
- All API calls, SSE connections, and webhook URLs use detected base path
- No change in behavior for root deployments

### Security Profile UX
- AI Builder defaults to Manual mode when system LLM is not configured
- Generate button disabled with tooltip when LLM unavailable
- Red inline warning: "System LLM not configured — AI Builder is disabled"

### NetworkPolicy Fix
- All policies scoped to `app.kubernetes.io/part-of: alcove` pods only
- Renamed to `alcove-default-deny`, `alcove-allow-internal`, `alcove-bridge-egress`
- Fixes issue where policies affected other apps in shared namespaces

### Bug Fixes
- JIRA credential incorrectly picked as LLM provider — excluded from
  FirstAvailableProvider query
- Missing GATE_TOOL_CONFIGS for JIRA — added tool config generation
- api_host URL cleanup (strip https:// prefix and trailing slash)
- Gate container missing home directory (useradd -m flag)
- Bridge container podman config dirs inaccessible (XDG_CONFIG_HOME fix)
- Credential form submit not firing in modal contexts — switched to
  button click handler

## v0.2.0

### System LLM Config-File-Only
- System LLM is now configured exclusively in `alcove.yaml` or via
  `BRIDGE_LLM_*` environment variables (no longer writable via dashboard or API)
- `PUT /api/v1/admin/settings/llm` returns 405 Method Not Allowed
- `GET /api/v1/admin/settings/llm` remains available (read-only status)
- Dashboard shows read-only system LLM status with guidance to edit
  `alcove.yaml` if not configured
- Two provider options: Anthropic (`llm_api_key`) and Google Vertex AI
  (`llm_service_account_json` + `llm_project` + `llm_region`)
- New env vars: `BRIDGE_LLM_SERVICE_ACCOUNT_JSON`, `BRIDGE_LLM_PROJECT`,
  `BRIDGE_LLM_REGION`, `BRIDGE_LLM_PROVIDER`, `BRIDGE_LLM_API_KEY`,
  `BRIDGE_LLM_MODEL`

### Red Hat Identity Auth Backend
- New `rh-identity` auth backend (`AUTH_BACKEND=rh-identity`) for Red Hat
  deployments behind Turnpike gateway
- Trusts `X-RH-Identity` header for authentication (no login form or passwords)
- JIT user provisioning from SAML identity (username, external_id, display_name)
- Admin bootstrap via `rh_identity_admins` in alcove.yaml or `RH_IDENTITY_ADMINS`
  env var

### JIRA/Atlassian Integration
- Gate proxies JIRA REST API via `/jira/` endpoint with full operation classification
- 12 builtin JIRA operations: read_issues, search_issues, read_comments, read_transitions,
  create_issue, update_issue, add_comment, assign_issue, transition_issue, add_worklog,
  move_to_sprint, delete_issue (plus read_projects, read_boards, read_sprints, read_metadata)
- Credential form accepts JIRA Cloud credentials (email + API token for Basic auth)
- Builtin "jira" tool seeded with operations and security profile support
- Project-key extraction from issue keys for per-project scope enforcement

### NetworkPolicy
- All policies scoped to `app.kubernetes.io/part-of: alcove` pods only to avoid
  affecting other applications in a shared namespace
- Policies renamed to `alcove-default-deny`, `alcove-allow-internal`, `alcove-bridge-egress`

### Kubernetes Runtime
- Kubernetes/OpenShift deployment support — Bridge creates k8s Jobs with Gate as
  a native sidecar (KEP-753), no operator or CRDs needed
- Minimal RBAC: namespace-scoped Role for jobs, pods, networkpolicies, secrets
- Per-task NetworkPolicy restricting egress to DNS, HTTPS, and internal services
- OpenShift restricted-v2 SCC compatible (non-root, drop all caps, seccomp)
- OpenShift deployment template and app-interface configuration for staging
- Resource requests/limits on dynamically created Job pods

### YAML Task Definitions
- Define reusable tasks in `.alcove/tasks/*.yml` files in git repos
- Register task repos (system-wide or per-user) via API and dashboard
- Auto-sync every 5 minutes with schedule reconciliation
- Starter templates: dependency audit, code review, test coverage analysis
- Run Now and View YAML from the dashboard

### GitHub Event Triggers
- Tasks triggered by GitHub webhook events (push, pull_request, issue_comment,
  release) alongside or instead of cron schedules
- HMAC-SHA256 webhook signature validation
- Idempotent delivery tracking via X-GitHub-Delivery header
- Configurable per-schedule: event filters by repo, branch, and action
- YAML trigger configuration in task definitions
- Webhook setup modal in dashboard with secret generation

### Skill/Agent Repos
- Configure git repos containing Claude Code skills, agents, and plugins
- System-wide (admin) and per-user configuration
- Repos cloned at Skiff startup and loaded via `--plugin-dir`
- New `user_settings` table for per-user configuration

### Dashboard
- Logo: nested waves design (favicon, login page, README)
- System LLM shown as read-only status in dashboard (configured via
  alcove.yaml only); shows guidance to edit alcove.yaml if not configured
- SCM options (GitHub/GitLab/Jira) filtered out of the system LLM provider dropdown
- Task Definitions section on Schedules page with source badges
- Skill / Agent Repos and Task Repos configuration modals
- Webhook configuration modal with setup instructions
- Trigger type selector (cron, event, both) on schedule form

### Configuration
- Config file switched from `alcove.conf` (KEY=VALUE) to `alcove.yaml` (YAML)
- `credential_key` renamed to `database_encryption_key` for clarity
- Env var renamed from `ALCOVE_CREDENTIAL_KEY` to `ALCOVE_DATABASE_ENCRYPTION_KEY`
- Bridge refuses to start without encryption key (no insecure default)
- `make up` auto-generates `alcove.yaml` with random key

### CI/CD
- GitHub Actions CI workflow (test + vet on push/PR)
- GitHub Actions Release workflow (build binaries, container images, GitHub Release)
- Container images published to ghcr.io/bmbouter/alcove-{bridge,gate,skiff-base}
- Version embedding via ldflags in all binaries
- Updated to Node.js 24-compatible GitHub Actions

### Bug Fixes
- Fix cancel topic mismatch (Bridge published to sessionID, Skiff subscribed to taskID)
- Add error logging for silently ignored errors across api.go, dispatcher.go, podman.go
- Fix SELinux label on alcove.yaml volume mount for Fedora/RHEL
- Fix `AUTH_BACKEND=postgres` missing from OpenShift template
- Fix `TaskStatus` treating all API errors as "not_found"
- Fix job name truncation producing trailing hyphens
- Strip trailing hyphens from job names after truncation

### Documentation
- API reference: added Tools, Profiles, Admin Settings, Skill / Agent Repos, Task Repos,
  Task Definitions, Task Templates, Webhook endpoints
- CLI reference: added --model and --budget flags
- Configuration guide: alcove.yaml format, Kubernetes secrets, skill/task repos
- Implementation status updated for all new features
- Test script documentation headers added
- CONTRIBUTING.md created
- Kubernetes deployment guide (RBAC, NetworkPolicy, OpenShift compatibility)

## v0.1.0

Initial release. Sandboxed AI coding agents on OpenShift/Kubernetes.

### Core Components
- **Bridge**: Controller with REST API, web dashboard, and task scheduler
- **Skiff**: Ephemeral Claude Code worker containers
- **Gate**: Auth proxy sidecar (LLM API proxy, SCM proxy, scope enforcement)
- **Hail**: NATS message bus for status updates and real-time streaming
- **Ledger**: PostgreSQL session store with transcripts and audit trails

### Features
- Ephemeral container execution: one task, one container, then destroy
- Podman dual-network isolation (internal + external) with Gate as bridge
- Credential management with AES-256-GCM encryption at rest
- OAuth2 token acquisition for Anthropic and Google Vertex AI
- Gate proxy: LLM API translation (Anthropic to Vertex AI), SCM proxy
- Security profiles with multi-rule per-repo operation scoping
- AI-powered security profile builder
- MCP tool gateway with builtin GitHub and GitLab tools
- Cron scheduler with NLP-style expression parsing
- Real-time transcript streaming via NATS + SSE
- Multi-user authentication with admin roles (memory and postgres backends)
- Self-service password change
- Session pagination and filtering
- CLI client (`alcove run`, `alcove list`, `alcove logs`, `alcove cancel`)
- Dashboard with guided setup checklist
- Apache 2.0 license
