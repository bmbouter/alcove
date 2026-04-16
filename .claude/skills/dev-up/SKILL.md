---
name: dev-up
description: Tear down and bring up a fresh local dev environment with credentials configured
user-invocable: true
---

Tear down everything, build locally, start fresh (~12 seconds), and configure default credentials.

## Steps

### 1. Tear down everything
```bash
pkill -f 'bin/bridge' 2>/dev/null
for c in $(podman ps -a --format "{{.Names}}" | grep -E "alcove|gate-|skiff-"); do podman rm -f "$c" 2>/dev/null; done
podman network rm alcove-internal alcove-external 2>/dev/null
```

### 2. Build and start
Run `make up`. This builds Go binaries locally, starts PostgreSQL + NATS containers, and runs Bridge as a local process (~12 seconds total).

For the old container-based approach (builds all 3 images, ~8 min), use `make up-full` instead.

### 3. Ensure Bridge is running
Check health within 10 seconds. The fast `make up` runs Bridge locally so it starts immediately.

If using postgres auth backend, restart Bridge manually with the backend flag:
```bash
make down
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=podman \
ALCOVE_NETWORK=alcove-internal \
ALCOVE_EXTERNAL_NETWORK=alcove-external \
AUTH_BACKEND=postgres \
ADMIN_RESET_PASSWORD=admin \
./bin/bridge
```

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
Read the GitHub PAT from `~/.config/alcove-github-token` or fall back to `gh auth token`:
```bash
GH_PAT=$(cat ~/.config/alcove-github-token 2>/dev/null || gh auth token 2>/dev/null)
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
