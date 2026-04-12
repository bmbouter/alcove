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

### Local Development (Podman/Docker)

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
