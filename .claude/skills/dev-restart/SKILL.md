---
name: dev-restart
description: Restart Bridge and Gate/Skiff containers while preserving the database
user-invocable: true
---

Restart Alcove containers while keeping PostgreSQL (database persists). No credential configuration needed since the DB retains all data.

## Steps

### 1. Stop non-database containers
```bash
for c in $(podman ps -a --format "{{.Names}}" | grep -E "alcove-bridge|alcove-hail|gate-|skiff-"); do podman rm -f "$c" 2>/dev/null; done
```
Do NOT stop `alcove-ledger` (PostgreSQL).

### 2. Rebuild images
```bash
make build-images
```

### 3. Ensure networks exist
```bash
podman network create --internal alcove-internal 2>/dev/null || true
podman network create alcove-external 2>/dev/null || true
```

### 4. Start NATS
```bash
podman run -d --rm --replace --name alcove-hail --network alcove-internal \
  -p 4222:4222 -p 8222:8222 docker.io/library/nats:latest
```

### 5. Start Bridge
```bash
VER=$(git describe --tags --always --dirty)
podman run -d --replace --name alcove-bridge \
  --network alcove-internal,alcove-external \
  -p 8080:8080 --user 0 --security-opt label=disable \
  -v ${XDG_RUNTIME_DIR}/podman/podman.sock:/run/podman/podman.sock \
  -v $(pwd)/web:/web:ro,z \
  -v $(pwd)/alcove.yaml:/etc/alcove/alcove.yaml:ro,z \
  -e CONTAINER_HOST=unix:///run/podman/podman.sock \
  -e LEDGER_DATABASE_URL=postgres://alcove:alcove@alcove-ledger:5432/alcove?sslmode=disable \
  -e HAIL_URL=nats://alcove-hail:4222 \
  -e RUNTIME=podman -e ALCOVE_WEB_DIR=/web \
  -e ALCOVE_NETWORK=alcove-internal -e ALCOVE_EXTERNAL_NETWORK=alcove-external \
  -e SKIFF_IMAGE=localhost/alcove-skiff-base:$VER \
  -e GATE_IMAGE=localhost/alcove-gate:$VER \
  localhost/alcove-bridge:$VER
```

### 6. Wait for health
Poll `curl http://localhost:8080/api/v1/health` until healthy.

### 7. Report
```
Dashboard: http://localhost:8080
Login: admin / admin (or existing credentials from DB)
Database: preserved
```
