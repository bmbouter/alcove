---
name: dev-restart
description: Rebuild and restart Bridge while preserving the database and all configuration
user-invocable: true
---

Rebuild and restart without wiping the database. All credentials, agent repos, sessions, and team configuration persist. Use this when you've made code changes and want to test them, or after adding a migration.

## Steps

### 1. Pull latest and clean up orphans
```bash
git checkout main && git pull
for c in $(podman ps -a --format "{{.Names}}" | grep -E "gate-|skiff-"); do podman rm -f "$c" 2>/dev/null; done
```

### 2. Rebuild and restart
```bash
make down   # stops Bridge + NATS + PostgreSQL, keeps DB volume
make up     # rebuilds binaries + images, restarts everything
```

### 3. Verify Bridge is healthy
```bash
curl -s http://localhost:8080/api/v1/health
```

No credential or agent repo configuration needed — the database retains everything.

If postgres auth backend was in use, Bridge needs `AUTH_BACKEND=postgres` — see the dev-up skill for the full command.

### 4. Report
```
Dashboard:   http://localhost:8080
Database:    preserved (all sessions, credentials, agent repos intact)
```
