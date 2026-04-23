# Upgrading Alcove

## Overview

Alcove can be upgraded while sessions are actively running. Bridge (the
controller) restarts with new code while existing Skiff+Gate containers
continue running undisturbed.

## What Happens During an Upgrade

1. **Running sessions continue** -- Skiff containers are independent
   processes. They are NOT affected by Bridge restarts.
2. **Bridge recovers state** -- On startup, Bridge queries the database
   for sessions still marked as "running" and checks their actual
   container status. Sessions whose containers have exited are
   automatically cleaned up.
3. **CI Gate monitors resume** -- Any PR monitoring that was in progress
   is automatically resumed from the database state.
4. **Events are not lost** -- GitHub events remain in the Events API.
   The poller fetches them on the next cycle. The dedup table prevents
   double-dispatch.
5. **New sessions use new images** -- After upgrade, new sessions launch
   with the new Skiff and Gate container images.

## Upgrade Procedure

### OpenShift/Kubernetes

Update the image tags in the deployment and apply. The rolling update
replaces Bridge while sessions continue:

```bash
# Update image tags in app-interface or deployment manifest
# Bridge restarts automatically
# Running sessions are unaffected
```

### Local Development (Podman)

```bash
make build-images
# Restart Bridge only -- running sessions continue
podman run -d --replace --name alcove-bridge ...
```

## Database Migrations

Migrations run automatically on Bridge startup. All migrations MUST be
additive (no column drops, no renames, no NOT NULL without defaults)
to ensure the old Bridge version can coexist during rolling updates.

## Session Reconciliation

Bridge runs a reconciliation loop every 2 minutes that:
- Queries sessions in "running" state
- Checks actual container/job status via the runtime API
- Marks exited containers as completed/error
- Recovers in-memory tracking for still-running containers

This ensures no session is stuck as "running" forever, even if a NATS
status update was lost during a Bridge restart.

## Maintenance Mode

Admins can pause session dispatching before an upgrade:

### API

```bash
# Pause dispatching
curl -X PUT /api/v1/admin/system-state -d '{"mode": "paused"}'

# Check status
curl /api/v1/admin/system-state
# {"mode": "paused", "running_sessions": 3}

# Resume after upgrade
curl -X PUT /api/v1/admin/system-state -d '{"mode": "active"}'
```

When paused:
- Scheduler skips cron dispatches
- Poller skips event processing (events remain in GitHub API)
- Manual dispatch returns 503
- Running sessions continue to completion
- Dashboard shows a maintenance banner

When resumed:
- Poller immediately fetches pending events
- Dedup table prevents double-dispatch
- Scheduler resumes from next_run

## Database Migration Policy

All database migrations MUST be additive to ensure safe rolling updates:

### Allowed
- `CREATE TABLE`
- `ALTER TABLE ADD COLUMN` (nullable or with default)
- `CREATE INDEX`
- New rows in reference tables

### Not Allowed
- `ALTER TABLE DROP COLUMN`
- `ALTER TABLE RENAME COLUMN`
- `ALTER TABLE ALTER COLUMN SET NOT NULL` (without default)
- `DROP TABLE`
- `ALTER TABLE RENAME`

During a rolling update, the old Bridge pod continues serving while the
new pod runs migrations. Both versions must be able to query the same
schema simultaneously. The advisory lock in `migrate.go` prevents
concurrent migration execution.

## Deployment Configuration

### OpenShift/Kubernetes

The Bridge Deployment includes:

- **preStop hook**: 5-second sleep before SIGTERM, allowing the load
  balancer to drain connections
- **terminationGracePeriodSeconds: 60**: Bridge has 60 seconds for
  graceful shutdown (HTTP server drain, NATS drain, final status updates)
- **Readiness probe**: `/api/v1/health` — new pod only receives traffic
  after database connectivity is confirmed
- **Rolling update strategy**: Old pod serves traffic while new pod
  starts, ensuring zero downtime

### Image Version Management

After upgrade:
- New sessions use the new `SKIFF_IMAGE` and `GATE_IMAGE`
- Old sessions continue with their original images until completion
- No version conflict — sessions are fully isolated containers

## Troubleshooting

### Sessions stuck as "running" after upgrade

The reconciliation loop (every 2 minutes) automatically detects and
cleans up these sessions. If a session is stuck for more than 5 minutes:

1. Check if the Skiff container still exists:
   ```bash
   oc get pods -l task-id=<task-id>
   ```

2. If the pod is gone, the next reconciliation cycle will clean it up.

3. To force cleanup immediately, restart Bridge — `RecoverHandles()`
   runs on startup.

### Events not being processed after upgrade

1. Check system mode: `GET /api/v1/admin/system-state`
2. If paused, resume: `PUT /api/v1/admin/system-state {"mode": "active"}`
3. Check poller logs: `oc logs deployment/alcove-bridge | grep poller`
4. Events in GitHub's API are retained for ~90 minutes. As long as
   Bridge resumes polling within that window, no events are lost.

### CI Gate monitors not resuming

CI Gate monitors are recovered on startup from the `ci_gate_state`
table. If a monitor isn't running:

1. Check the state: `SELECT * FROM ci_gate_state WHERE status = 'monitoring'`
2. Restart Bridge to trigger `RecoverMonitors()`
