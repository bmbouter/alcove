# Changelog

All notable changes to Alcove are documented here. This project uses
[Semantic Versioning](https://semver.org/).

## v0.18.0

### Features
- Add workflow graph v2: bridge actions, bounded cycles, depends
  expressions, minimal prompts, workflow migration, and authoring guide.

### Bug Fixes
- Fix Docker network syntax: use separate --network flags instead of
  comma-separated, fixing compatibility with QNAP Docker.
- Fix default Gate and Skiff image refs to use GHCR instead of
  localhost/dev, fixing standalone Docker deployments.
- Remove stale skill-repo references from docs and CSS.

## v0.17.0

### Features
- Add browsable Catalog for plugins, integrations, and agent templates.
  Teams browse a filterable card grid and toggle entries on/off. Replaces
  the removed Skill/Agent Repos system. Starter catalog includes 9
  entries from Anthropic official plugins, LSPs, and community repos.
- Add CLI commands: `alcove catalog list`, `enable`, `disable`.
- Dispatcher resolves enabled catalog entries into ALCOVE_SKILL_REPOS
  for Skiff sessions.

## v0.16.0

### Changes
- Consolidate dashboard navigation: remove Teams tab from top nav (use
  team switcher dropdown → Manage Teams instead), remove duplicate Agent
  Repos from team detail page, remove Skill/Agent Repos section from
  Repos page (to be replaced by Catalog feature).
- Remove skill-repos API endpoints (/api/v1/admin/settings/skill-repos
  and /api/v1/user/settings/skill-repos). These are replaced by the
  upcoming Catalog feature.
- Add Docker CLI to Bridge container image.

## v0.15.6

### Bug Fixes
- Fix session list not showing completed sessions. The frontend sent
  comma-separated status values (completed,error,cancelled,timeout) but
  the backend did an exact match instead of IN (...), returning 0 results.

## v0.15.5

### Bug Fixes
- Fix workflow steps display showing "0 steps / No steps defined". The
  JS read from a non-existent `steps` field instead of the `workflow`
  array returned by the API.

## v0.15.4

### Bug Fixes
- Fix team scoping race condition in dashboard. loadTeams() was called
  on every route change, temporarily nulling activeTeamId while async.
  Other load functions fired during this window without the team header,
  causing all pages to show the personal team's data regardless of
  which team was selected.

## v0.15.3

### Bug Fixes
- Fix team scoping in dashboard. When team resolution failed, the auth
  middleware silently skipped setting the team header, causing API handlers
  to return all data instead of team-scoped results. Now logs errors and
  falls back to the personal team.

## v0.15.2

### Bug Fixes
- Fix workflow sync: add missing definition column to INSERT. Workflow
  definitions were never stored because UpsertWorkflow omitted a NOT NULL
  column, causing every insert to silently fail.

## v0.15.1

### Bug Fixes
- Fix team switching in dashboard showing stale cached data. Add
  Cache-Control: no-store to all API responses so the browser fetches
  fresh data when switching teams.
- Fix repo sync ignoring non-personal teams. Agent repos configured on
  shared teams were never synced because the query filtered on
  is_personal=true.
- Fix config validation rejecting "docker" as a valid container runtime.

## v0.10.0

### Features
- Add CI Gate: Bridge-driven CI retry loop for autonomous developer tasks.
  When a agent definition includes `ci_gate`, Bridge monitors CI status on PRs
  created by the task and automatically dispatches fresh retry agents on failure,
  using the system LLM to analyze failure logs and compose targeted fix prompts.
- Add Go 1.25 toolchain to Skiff base image. Autonomous agents can now run
  `go build`, `go vet`, and `go test` locally before pushing, catching
  compilation and test errors without waiting for CI.
- Rewrite autonomous developer prompt with mandatory pre-push validation,
  structured CI retry discipline, and risk assessment for untestable changes.

## v0.9.1

### Bug Fixes
- Fix GitHub Events API poller reliability by removing all ID-based event
  filtering. GitHub event IDs are not chronologically ordered, causing the
  poller to skip valid events. The poller now processes all fetched events
  and relies solely on the webhook_deliveries table for deduplication.

## v0.9.0

### Features
- Add enable/disable toggle for agent repos. When multiple Alcove instances
  share the same agent repo, disable it on one instance to prevent both
  from competing for the same events. Toggle via checkbox in the Repos page.

### Bug Fixes
- Fix GitHub poll ETags cleared on startup to prevent stale 304 loops.
  After deployments, the poller could get permanently stuck receiving
  304 Not Modified from GitHub's CDN. Now automatically clears stale
  ETags on every Bridge startup.

## v0.8.0

### Features
- Add Token Based Registry (TBR) identity association for rh-identity
  auth backend. SSO users can associate TBR identities with their account,
  enabling API authentication via TBR tokens. Includes database migration,
  REST API endpoints, dashboard Account page, and extensive logging.

## v0.7.1

### Bug Fixes
- Fix GitHub Events API poller to paginate through all available events
  instead of only fetching page 1 (30 events). The poller now fetches up
  to 10 pages (300 events), stopping when it reaches an already-seen event.
  This prevents events from being permanently missed during high activity.
- Fix event ID comparison to use numeric (int64) comparison instead of
  lexicographic string comparison. String comparison caused incorrect
  ordering where "9" was greater than "10000000", silently skipping events.

## v0.7.0

### Features
- Add north-star security principles to README and dedicated design doc
  with implementation details, cross-references, and threat model summary.
- Add informational banner to dashboard when using rh-identity auth backend
  with links to issue tracker and security principles.
- Autonomous developer agent now self-assigns issues it works on.
- PR reviewer agent now self-assigns as reviewer on PRs it reviews.

### Bug Fixes
- Fix duplicate session dispatches when GitHub fires multiple events for the
  same issue (e.g., opened + labeled). Poller now deduplicates by issue/PR
  number within a single poll cycle.
- Add missing `create_review` operation to alcove-reviewer security profile.

## v0.6.1

### Bug Fixes
- Fix poller label extraction for `labeled` events. The GitHub Events API
  includes the added label in `payload.label`, not in `pull_request.labels`
  (which can be empty). The poller now checks both sources, fixing the PR
  reviewer agent not triggering on `awaiting-review` labels.

## v0.6.0

### Features
- Add PR review workflow. New reviewer agent reviews PRs after CI passes,
  can approve and merge or request changes. Autonomous dev agent handles
  revision cycles. 3-cycle limit before escalating to human review.
- New GitHub Actions workflow adds `awaiting-review` label when CI passes
  on PRs, triggering the reviewer agent automatically.
- Updated deploy-staging and release skills to match automated workflows.

### Bug Fixes
- Fix event metadata not reaching tasks. The poller was appending event
  context (issue number, PR number, etc.) to the database after the session
  container had already launched. Metadata is now included in the prompt
  before dispatch.
- Fix label-on-ci-pass workflow permissions (needs `issues: write`).

## v0.5.0

### Features
- Add automated release agent agent definition. Runs daily at 6 AM UTC
  and on-demand via `immediate-release` label. Handles changelog generation,
  PR creation, CI monitoring, tagging, and release build verification.
- Improve transcript readability with collapsible sections and visual
  hierarchy. Tool calls and results are collapsed by default, making
  long transcripts easier to navigate.

### Bug Fixes
- Fix event metadata to include GITHUB_ISSUE_NUMBER for issue events,
  enabling event-triggered tasks to identify the correct issue.
- Add `labeled` action to autonomous-dev trigger. Previously, adding
  the `ready-for-dev` label didn't trigger the agent.

### Improvements
- Revise automated release agent trigger configuration: daily cron
  schedule plus immediate-release label trigger.
- Remove issue-triage task (superseded by autonomous-dev).

## v0.4.13

### Features
- Add label-based trigger filtering. Event triggers can now require specific
  labels (e.g., `labels: [ready-for-dev]`). Tasks only dispatch when the
  issue/PR has a matching label. Enforced at the trigger level, not in the
  prompt — prevents unauthorized issues from triggering automated development.

## v0.4.15

### Features
- Add user-based trigger filtering. Event triggers can specify `users`
  to only fire for events from specific usernames. Prevents tasks from
  re-triggering on their own comments (e.g., planner posting a plan).

## v0.4.14

### Bug Fixes
- Fix Gate URLs on Kubernetes: override GITHUB_API_URL, GITLAB_API_URL,
  JIRA_API_URL, and GH_HOST to use localhost:8443 instead of the Gate
  container hostname (which doesn't resolve on Kubernetes where Gate is
  a native sidecar sharing the pod network namespace).

## v0.4.12

### Improvements
- Replace transcript streaming with 5-second polling. Streaming through
  Akamai/Turnpike is not viable (v0.4.2-v0.4.11 investigation). Polling
  works reliably — identical to how proxy log already works. Transcript
  and proxy log both update every ~5 seconds during running sessions.

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
- Add autonomous developer agent definition and alcove-developer security profile
  for fully autonomous software development lifecycle.

## v0.4.0

### Features
- **YAML security profiles**: Define security profiles in `.alcove/security-profiles/*.yml`
  alongside agent definitions. Synced from agent repos, read-only in UI, with profile
  validation on agent definitions (sync errors block dispatch).
- **GitHub event polling**: Poll GitHub Events API every 60 seconds for event-triggered
  tasks. Works in local dev without webhooks. Supports ETag conditional requests,
  deduplication, and per-user credentials.
- **Per-user resource ownership**: All resources (agent definitions, schedules, security
  profiles, sessions) are owned by real users. Removed `_system` submitter concept.
  Strict user isolation across all pages.
- **Repos page**: New top-level nav tab for managing Agent Repos and Skill / Agent Repos
  (moved from dropdown menu).
- **Proxy log filtering and sorting**: Clickable column headers for sorting, dropdown
  filters for Service and Decision, summary counts.
- **Agent definition cards**: Show security profiles, schedule with next/last run times,
  event triggers, and sync errors with disabled Run button.
- **PR review template**: New starter template for event-triggered PR reviews.

### UI/UX Improvements
- Renamed Profiles section to Security, Sessions to Tasks in dashboard.
- Unified Schedules page (agent definitions + manual schedules in one list).
- Unified Security page (all profiles in one list, no section separators).
- Session pagination (15 per page) with relative timestamp "When" column.
- New Task tab moved to leftmost nav position, admin tabs right-aligned.
- Run Now shows "View Task" link after dispatch.
- Live indicator hidden for completed tasks.
- Renamed Skill Repos to Skill / Agent Repos.

### Backend Changes
- Removed builtin security profiles (replaced by YAML-defined profiles from repos).
- Removed system agent repos (all repos are per-user).
- Added `GH_PROTOCOL=http` for gh CLI through Gate proxy.
- Migration 014: YAML profile source tracking columns.
- Migration 015: Agent definition owner column with per-user source keys.
- Migration 016: GitHub poll state table for ETag and event ID tracking.

### Bug Fixes
- Fix proxy log: gateEnvVars built before URL resolution.
- Fix SyncAll cleanup for users with empty repo list.
- Fix profile validation setting empty owner on agent definitions.
- Fix `notsecret` annotations on test credentials.

### CI/CD
- Added functional tests to CI (70+ API tests across 5 scripts).
- Added `go vet` to release workflow.
- Test scripts use per-user agent repos endpoint.

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

### YAML Agent Definitions
- Define reusable tasks in `.alcove/tasks/*.yml` files in git repos
- Register agent repos (system-wide or per-user) via API and dashboard
- Auto-sync every 5 minutes with schedule reconciliation
- Starter templates: dependency audit, code review, test coverage analysis
- Run Now and View YAML from the dashboard

### GitHub Event Triggers
- Tasks triggered by GitHub webhook events (push, pull_request, issue_comment,
  release) alongside or instead of cron schedules
- HMAC-SHA256 webhook signature validation
- Idempotent delivery tracking via X-GitHub-Delivery header
- Configurable per-schedule: event filters by repo, branch, and action
- YAML trigger configuration in agent definitions
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
- Agent Definitions section on Schedules page with source badges
- Skill / Agent Repos and Agent Repos configuration modals
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
- API reference: added Tools, Profiles, Admin Settings, Skill / Agent Repos, Agent Repos,
  Agent Definitions, Agent Templates, Webhook endpoints
- CLI reference: added --model and --budget flags
- Configuration guide: alcove.yaml format, Kubernetes secrets, skill/agent repos
- Implementation status updated for all new features
- Test script documentation headers added
- CONTRIBUTING.md created
- Kubernetes deployment guide (RBAC, NetworkPolicy, OpenShift compatibility)

## v0.1.0

Initial release. Sandboxed AI coding agents on OpenShift/Kubernetes.

### Core Components
- **Bridge**: Controller with REST API, web dashboard, and session scheduler
- **Skiff**: Ephemeral Claude Code worker containers
- **Gate**: Auth proxy sidecar (LLM API proxy, SCM proxy, scope enforcement)
- **Hail**: NATS message bus for status updates and real-time streaming
- **Ledger**: PostgreSQL session store with transcripts and audit trails

### Features
- Ephemeral container execution: one session, one container, then destroy
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
