---
name: dev-up
description: Tear down and bring up a fresh local dev environment with credentials configured
user-invocable: true
---

Tear down ALL containers (including database), rebuild images, start fresh, and configure default credentials.

## Steps

### 1. Tear down everything
```bash
for c in $(podman ps -a --format "{{.Names}}" | grep -E "alcove|gate-|skiff-"); do podman rm -f "$c" 2>/dev/null; done
podman network rm alcove-internal alcove-external 2>/dev/null
```

### 2. Build and start
Run `make up`. This builds all images and starts PostgreSQL, NATS, and Bridge.

### 3. Ensure Bridge is running
Bridge often fails to start due to a race with PostgreSQL. Check health within 10 seconds. If not healthy, restart Bridge manually:
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
Wait for health check to pass.

### 4. Configure admin Vertex AI credential
Since the database is fresh, configure the Vertex AI user credential for admin:

```bash
TOKEN=$(curl -s http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")
```

Read the service account JSON from alcove.yaml (same credentials as the system LLM):
```python
import yaml
with open('alcove.yaml') as f:
    cfg = yaml.safe_load(f)
sa_json = cfg.get('system_llm', {}).get('service_account_json', '')
project_id = cfg.get('system_llm', {}).get('project_id', '')
region = cfg.get('system_llm', {}).get('region', '')
```

POST the credential:
```bash
curl -s -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Vertex AI","provider":"google-vertex","auth_type":"service_account","credential":"<SA_JSON>","project_id":"<PROJECT_ID>","region":"<REGION>"}'
```

### 5. Configure admin GitHub credential
Read the GitHub PAT from `~/.config/alcove-github-token` and POST it:
```bash
GH_PAT=$(cat ~/.config/alcove-github-token)
curl -s -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"github\",\"provider\":\"github\",\"auth_type\":\"api_key\",\"credential\":\"$GH_PAT\"}"
```

### 6. Report
```
Dashboard: http://localhost:8080
Login: admin / admin
Vertex AI credential: configured
GitHub credential: configured
System LLM: configured (from alcove.yaml)
```
