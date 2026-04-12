# Security Principles

## Overview

Alcove's security model is built around five north-star principles that define how AI coding agents are safely executed in production environments. These principles guide every architectural decision and implementation detail across the platform.

## 1. Safe Sandboxing

**Principle:** Every session runs in an ephemeral, network-isolated container. Containers are never reused across sessions. NetworkPolicy (OpenShift) and dual-network isolation (podman) enforce this.

### Implementation

**Ephemeral Containers (Skiff):**
- Each session spawns a fresh Kubernetes Job or podman container
- Containers are destroyed immediately after session completion (success or failure)
- No persistent filesystem state survives between sessions
- Container images are read-only base layers plus task-specific volumes

**Network Isolation:**
- **Kubernetes:** NetworkPolicy restricts Skiff pod egress to only the Gate sidecar and essential package repositories
- **Podman:** iptables rules and network namespaces prevent direct external access
- All external network traffic must flow through Gate proxy for authorization

**Resource Limits:**
- CPU and memory limits prevent resource exhaustion attacks
- Disk space quotas prevent filesystem DoS
- Time-based timeouts prevent infinite execution

**Container Security:**
- Non-root execution with minimal privileges
- No privileged escalation capabilities
- Read-only root filesystem where possible
- Seccomp and AppArmor/SELinux profiles (where available)

See [Problem Statement](problem-statement.md) for the rationale behind ephemeral execution.

## 2. Data Leakage Protection

**Principle:** Real credentials (API keys, tokens, passwords) are never available to the LLM or the sandboxed environment. Gate injects credentials at the proxy level; Skiff only ever sees dummy tokens.

### Implementation

**Credential Separation:**
- Real credentials stored encrypted in Bridge's PostgreSQL database
- Gate receives real credentials via environment variables from Bridge
- Skiff receives dummy tokens (random UUIDs with `alcove-session-` prefix)
- LLM never sees real credential values in any context

**Token Replacement Architecture:**
```
Bridge (stores encrypted real credentials)
   ↓ (encrypted env vars)
Gate (receives real credentials, issues dummy tokens to Skiff)
   ↓ (dummy tokens)
Skiff (LLM sees only dummy tokens)
   ↓ (dummy token in HTTP requests)
Gate (replaces dummy with real credentials before external API calls)
   ↓ (real credentials)
External APIs (GitHub, GitLab, etc.)
```

**Dummy Token Properties:**
- Format: `alcove-session-<random-uuid>`
- Meaningless outside the Gate sidecar context
- Cannot be used against any real API
- Different dummy token per service (GitHub, GitLab, etc.)
- Session-scoped lifetime

**Credential Encryption:**
- AES-256-GCM encryption for credentials at rest
- Master key via `ALCOVE_DATABASE_ENCRYPTION_KEY` environment variable
- Credentials decrypted only in Bridge memory during task dispatch
- No credential material in logs, error messages, or API responses

See [Credential Management](credential-management.md) for detailed implementation.

## 3. Human-in-the-Loop Controls

**Principle:** Security profiles define what each session is allowed to do. Scope enforcement, session approval, and label-gated triggers ensure humans remain in control of what automated agents can access and modify.

### Implementation

**Security Profiles:**
- Named, reusable permission bundles stored per-user
- Define allowed repositories, operations, and service access
- Multiple profiles can be stacked (union semantics)
- System-wide profiles for organizational defaults
- Profile changes require explicit user action

**Scope Enforcement:**
- Every session specifies its required scope at submission time
- Bridge validates scope against available credentials and user permissions
- Gate enforces scope at runtime with operation-level granularity
- No scope escalation possible once session is running

**Operation Classification:**
- **Read operations:** `clone`, `fetch`, `read_prs`, `read_issues`, `read_contents`
- **Write operations:** `create_pr_draft`, `create_pr`, `push_branch`, `create_comment`
- **Dangerous operations:** `merge_pr`, `delete_branch`, `write_releases`

**Approval Gates:**
- Ready-for-dev label requirement for autonomous issue processing
- Human review required for dangerous operations
- Pull request review workflow for code changes
- Session termination controls in dashboard

**Repository Access Controls:**
- Explicit repository allowlists (no wildcard defaults)
- Organization-level access patterns (`org/*`) where appropriate
- Cross-repository access requires explicit permission
- Private repository access tied to credential ownership

See [Security Profiles and System LLM](security-profiles-and-system-llm.md) for profile design and [Gate SCM Authorization](gate-scm-authorization.md) for operation enforcement.

## 4. Audit Records

**Principle:** Every network request through Gate is logged with full details (service, method, path, decision, response). All LLM tool use, inputs, and outputs are captured in session transcripts stored in the database.

### Implementation

**Comprehensive Logging:**
- All HTTP requests proxied through Gate are logged
- Request method, URL, headers (sanitized), response status
- Authorization decisions with detailed reasoning
- Credential injection events (without credential values)
- Network tunnel creation and termination

**Session Transcripts:**
- Complete LLM conversation history per session
- Tool invocations with input parameters and outputs
- Error conditions and retry attempts
- Session start/completion timestamps and status
- Resource usage metrics (CPU, memory, duration)

**Structured Audit Data:**
```json
{
  "timestamp": "2026-03-25T10:30:00Z",
  "session_id": "abc-123",
  "method": "POST",
  "url": "https://api.github.com/repos/org/repo/pulls",
  "service": "github",
  "operation": "create_pr",
  "decision": "allow",
  "status_code": 201,
  "user": "developer@company.com",
  "scope": {...}
}
```

**Data Retention:**
- Session data retained per organizational policy
- Configurable retention periods (30/90/365 days)
- Secure deletion with cryptographic erasure
- Export capabilities for compliance requirements

**Dashboard Analytics:**
- Session search and filtering
- Operation frequency analysis
- Security event detection
- User activity reports

See existing session storage implementation in Bridge's PostgreSQL schema.

## 5. Least Privilege

**Principle:** Gate's network proxy is DENY by default. Only explicitly allowed operations (defined in security profiles) are permitted. Sessions cannot reach arbitrary network endpoints.

### Implementation

**Default Deny Network Policy:**
- All external network access blocked by default
- Explicit allowlist required for every operation
- Service-specific operation granularity (not just host-level access)
- No ambient network permissions

**Operation-Level Authorization:**
- HTTP method + URL path analyzed to determine operation type
- Each operation checked against scope before execution
- No operation escalation within approved scope
- Detailed rejection reasons logged

**Service-Specific Controls:**
- **GitHub:** `read_prs`, `create_pr_draft`, `create_pr`, `merge_pr`, etc.
- **GitLab:** `read_mrs`, `create_mr`, `approve_mr`, `clone`, `push_branch`, etc.
- **JIRA:** `read_issues`, `create_issue`, `transition_issue`, etc.
- **Package Managers:** Limited to approved registries

**Network Segmentation:**
- Skiff can only reach Gate sidecar (same pod/network namespace)
- Gate can only reach explicitly approved external services
- No cross-session network communication
- No persistent network connections

**Credential Scoping:**
- Credentials limited to approved repositories/projects
- Token scopes minimized (read-only when possible)
- Per-session credential isolation
- No shared credential state

**Example Scope Validation:**
```json
{
  "services": {
    "github": {
      "repos": ["org/specific-repo"],
      "operations": ["clone", "read_prs", "create_pr_draft"]
    }
  }
}
```

This scope allows:
- ✅ `GET /repos/org/specific-repo/pulls`
- ✅ `POST /repos/org/specific-repo/pulls` with `draft: true`
- ❌ `POST /repos/org/specific-repo/pulls` with `draft: false`
- ❌ `GET /repos/other-org/other-repo/pulls`
- ❌ `PUT /repos/org/specific-repo/pulls/123/merge`

See [Gate SCM Authorization](gate-scm-authorization.md) for detailed operation classification and [Auth Backends](auth-backends.md) for credential scoping.

## Cross-Cutting Security Measures

### Defense in Depth

The five principles work together to create multiple security barriers:

1. **Container isolation** prevents lateral movement
2. **Network controls** prevent data exfiltration  
3. **Credential separation** limits blast radius
4. **Audit logging** enables incident response
5. **Scope enforcement** prevents privilege escalation

### Threat Model Coverage

**Prompt Injection Attacks:**
- Network isolation prevents arbitrary external calls
- Scope enforcement limits available operations
- Credential separation prevents real token exposure
- Audit logs detect anomalous behavior

**Container Escape:**
- Ephemeral execution limits persistence
- Network policies prevent external communication
- Resource limits prevent DoS
- Non-root execution reduces attack surface

**Credential Theft:**
- Dummy tokens are worthless outside Gate context
- Real credentials never enter untrusted containers
- Encrypted storage with key separation
- Minimal credential lifetime and scope

**Insider Threats:**
- Audit trails provide accountability
- Scope profiles limit access scope
- Human approval gates for sensitive operations
- Role-based access controls

### Compliance Considerations

The security principles align with common compliance frameworks:

- **SOC 2 Type II:** Audit logging, access controls, monitoring
- **ISO 27001:** Risk management, access control, cryptography
- **NIST Cybersecurity Framework:** Identify, Protect, Detect, Respond, Recover
- **GDPR:** Data minimization, purpose limitation, audit trails

## Related Documentation

- [Architecture Overview](architecture.md)
- [Credential Management](credential-management.md)
- [Gate SCM Authorization](gate-scm-authorization.md)
- [Security Profiles and System LLM](security-profiles-and-system-llm.md)
- [Auth Backends](auth-backends.md)
- [Problem Statement](problem-statement.md)
