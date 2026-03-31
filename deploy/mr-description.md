## Summary

Deploy [Alcove](https://github.com/bmbouter/alcove) into the `pulp-stage` namespace on `crcs02ue1`. Alcove runs sandboxed AI coding agents (Claude Code) in ephemeral, network-isolated containers on OpenShift.

## What is Alcove?

Alcove orchestrates AI coding agents in ephemeral containers with:
- **Bridge**: Controller with REST API, dashboard, and task scheduler
- **Skiff**: Ephemeral worker containers running Claude Code (created as k8s Jobs per task)
- **Gate**: Auth proxy sidecar (LLM API proxy, SCM proxy, scope enforcement)
- **Hail**: NATS message bus for real-time streaming
- **Ledger**: PostgreSQL for session storage, transcripts, audit trails

Each task gets a fresh container, a scoped authorization proxy, and a complete session transcript. No persistent state crosses task boundaries.

## Changes

### New files

| File | Purpose |
|------|---------|
| `data/services/pulp/deploy-alcove.yml` | SaaS file (saas-file-2 schema) deploying Alcove via the [OpenShift template](https://github.com/bmbouter/alcove/blob/main/deploy/openshift/template.yaml) from the Alcove GitHub repo |
| `resources/terraform/resources/pulp/stage/rds-alcove-stage.yml` | RDS defaults — PostgreSQL 16, db.t3.medium, 20GB, encrypted, single-AZ for staging |
| `resources/pulp-stage/alcove-config.secret.yaml` | Resource template (extracurlyjinja2) that constructs the `alcove-config` Secret from vault + ERv2 RDS outputs |

### Modified files

| File | Change |
|------|--------|
| `data/services/pulp/app.yml` | Add `alcove` codeComponent pointing to github.com/bmbouter/alcove |
| `data/services/pulp/namespaces/pulp-stage.yml` | Add `alcove-stage` RDS external resource with ERv2. Output secret: `alcove-db` |
| `data/services/pulp/namespaces/shared-resources/stage.yml` | Add `alcove-config` resource-template reference |

## How the alcove-config secret is constructed

The `alcove-config.secret.yaml` resource template uses the `vault()` function to:
- Read `database-encryption-key` from `app-interface/pulp/stage/alcove-config` in vault (manually created)
- Construct `ledger-database-url` from the ERv2 RDS output fields (`db.host`, `db.port`, `db.name`, `db.user`, `db.password`) at `app-sre/integrations-output/external-resources/crcs02ue1/pulp-stage/alcove-db`

Bridge reads both values from the resulting Kubernetes Secret via `secretKeyRef`.

## What gets deployed

The [OpenShift template](https://github.com/bmbouter/alcove/blob/main/deploy/openshift/template.yaml) creates:

- **Bridge Deployment** (1 replica) — `ghcr.io/bmbouter/alcove-bridge:0.1.0`
- **NATS Deployment** (1 replica) — `docker.io/library/nats:latest` (stateless messaging)
- **Services** — `alcove-bridge:8080`, `alcove-hail:4222`
- **RBAC** — ServiceAccount `alcove-bridge` with minimal Role (batch/jobs, pods, networkpolicies, secrets)
- **NetworkPolicies** — default deny, allow intra-namespace + DNS, Bridge external egress

Bridge dynamically creates **k8s Jobs** for each task (Skiff worker + Gate sidecar using native sidecar containers).

## Container images

All images are public on ghcr.io:
- `ghcr.io/bmbouter/alcove-bridge:0.1.0`
- `ghcr.io/bmbouter/alcove-gate:0.1.0`
- `ghcr.io/bmbouter/alcove-skiff-base:0.1.0`

## Prerequisites before merge

1. **Create vault secret** at `app-interface/pulp/stage/alcove-config` with one key:
   - `database-encryption-key`: AES-256 encryption key. Generate with: `openssl rand -hex 32`

2. The `ledger-database-url` is **automatically constructed** from the ERv2 RDS output — no manual URL needed.

## RBAC permissions (minimal)

| Resource | Verbs | Purpose |
|----------|-------|---------|
| `batch/jobs` | create, delete, get, list, watch | Create/monitor/cancel Skiff task Jobs |
| `core/pods` | get, list, watch | Watch pod status for task completion |
| `core/pods/log` | get | Debug log access |
| `networking.k8s.io/networkpolicies` | create, delete, get | Per-task network isolation |
| `core/secrets` | get | Read credentials for Gate injection |

## Rollback

Delete the Alcove resources without affecting Pulp:
```
oc delete deployment alcove-bridge alcove-hail -n pulp-stage
oc delete service alcove-bridge alcove-hail -n pulp-stage
oc delete sa alcove-bridge -n pulp-stage
oc delete role,rolebinding alcove-bridge -n pulp-stage
oc delete networkpolicy alcove-default-deny alcove-allow-namespace alcove-bridge-egress -n pulp-stage
```
