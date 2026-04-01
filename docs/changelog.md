# Changelog

All notable changes to Alcove are documented here. This project uses
[Semantic Versioning](https://semver.org/).

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
- Skill Repos and Task Repos configuration modals
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
- API reference: added Tools, Profiles, Admin Settings, Skill Repos, Task Repos,
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
