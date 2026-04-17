---
name: dev-up
description: Tear down and bring up a fresh local dev environment from scratch (wipes database)
user-invocable: true
---

Fresh dev environment from scratch. Wipes the database. Every step must succeed before proceeding.

## Step 1: Pull latest

```bash
git checkout main && git pull
```

## Step 2: Stop everything and wipe the database

```bash
make down
for c in $(podman ps -a --format "{{.Names}}" | grep -E "gate-|skiff-"); do podman rm -f "$c" 2>/dev/null; done
podman volume rm -f alcove-ledger-data
```

Verify the volume is gone:
```bash
podman volume ls | grep alcove
```
Must return empty. If not, stop and debug before continuing.

## Step 3: Build and start

```bash
make up
```

Wait 5 seconds, then verify Bridge is running:
```bash
podman ps --filter name=alcove-bridge --format "{{.Names}} {{.Status}}"
```

Must show `alcove-bridge Up`. If Bridge isn't running, check logs with:
```bash
podman logs alcove-bridge 2>&1 | tail -20
```

If the container doesn't exist at all (`--rm` cleaned it up after a crash), rerun without `--rm` to capture logs:
```bash
VER=$(git describe --tags --always --dirty)
podman run -d --replace --name alcove-bridge \
  --network alcove-internal,alcove-external -p 8080:8080 \
  --user 0 --security-opt label=disable \
  -v ${XDG_RUNTIME_DIR}/podman/podman.sock:/run/podman/podman.sock \
  -v $(pwd)/bin/bridge:/usr/local/bin/bridge:ro,z \
  -v $(pwd)/web:/web:ro,z \
  $(test -f alcove.yaml && echo "-v $(pwd)/alcove.yaml:/etc/alcove/alcove.yaml:ro,z") \
  -e CONTAINER_HOST=unix:///run/podman/podman.sock \
  -e LEDGER_DATABASE_URL=postgres://alcove:alcove@alcove-ledger:5432/alcove?sslmode=disable \
  -e HAIL_URL=nats://alcove-hail:4222 \
  -e RUNTIME=podman -e ALCOVE_WEB_DIR=/web \
  -e ALCOVE_NETWORK=alcove-internal -e ALCOVE_EXTERNAL_NETWORK=alcove-external \
  -e SKIFF_IMAGE=localhost/alcove-skiff-base:$VER \
  -e GATE_IMAGE=localhost/alcove-gate:$VER \
  localhost/alcove-bridge:$VER
```

Wait for PostgreSQL to be ready before proceeding:
```bash
for i in $(seq 1 15); do podman exec alcove-ledger pg_isready -U alcove -q 2>/dev/null && break; sleep 1; done
```

Then verify health:
```bash
curl -s http://localhost:8080/api/v1/health
```

## Step 4: Log in

**IMPORTANT:** Steps 4-7 all use `$TOKEN`. They must be run in the same shell invocation (chain with `&&`) so the variable persists.

```bash
TOKEN=$(curl -s http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")
```

Verify TOKEN is not empty before continuing. If empty, the Bridge may not be ready yet or the auth backend may not have seeded the admin user. Check `podman logs alcove-bridge 2>&1 | tail -20`.

## Step 5: Create LLM credential from .dev-credentials.yaml

Read `.dev-credentials.yaml` to determine the LLM provider and create the appropriate credential. This file is the single source of truth for developer credentials.

Run this Python script to parse `.dev-credentials.yaml` and create the credential via curl. The script receives the auth token via the `ALCOVE_TOKEN` environment variable (no temp files).

```bash
ALCOVE_TOKEN="$TOKEN" python3 -c "
import yaml, json, subprocess, os, sys

token = os.environ['ALCOVE_TOKEN']

creds_file = '.dev-credentials.yaml'
if not os.path.exists(creds_file):
    print('SKIP: .dev-credentials.yaml not found.')
    print('To fix: cp .dev-credentials.yaml.example .dev-credentials.yaml and fill in your values.')
    sys.exit(0)

with open(creds_file) as f:
    creds = yaml.safe_load(f) or {}

llm = creds.get('llm')
if not llm or not llm.get('provider'):
    print('SKIP: No llm section in .dev-credentials.yaml. Sessions will not have LLM access.')
    print('To fix: edit .dev-credentials.yaml and configure the llm section (see .dev-credentials.yaml.example).')
    sys.exit(0)

provider = llm['provider']

if provider == 'anthropic':
    payload = json.dumps({
        'name': 'Anthropic',
        'provider': 'anthropic',
        'auth_type': 'api_key',
        'credential': llm.get('api_key', ''),
    })
elif provider == 'claude-oauth':
    payload = json.dumps({
        'name': 'Claude OAuth',
        'provider': 'claude-oauth',
        'auth_type': 'api_key',
        'credential': llm.get('oauth_token', ''),
    })
elif provider == 'google-vertex':
    payload = json.dumps({
        'name': 'Vertex AI',
        'provider': 'google-vertex',
        'auth_type': 'service_account',
        'credential': llm.get('service_account_json', ''),
        'project_id': llm.get('project_id', ''),
        'region': llm.get('region', ''),
    })
else:
    print(f'ERROR: unknown llm provider: {provider}')
    sys.exit(1)

r = subprocess.run(['curl', '-s', '-X', 'POST',
    'http://localhost:8080/api/v1/credentials',
    '-H', f'Authorization: Bearer {token}',
    '-H', 'Content-Type: application/json',
    '-d', payload], capture_output=True, text=True)
print(r.stdout)
if r.returncode != 0:
    print(f'curl error: {r.stderr}', file=sys.stderr)
    sys.exit(1)
"
```

Verify it was created (should print JSON with an `id` field, not an error). If `.dev-credentials.yaml` is missing or has no llm section, the script prints a SKIP message -- that is acceptable but sessions will not have LLM access until the developer configures it.

## Step 6: Create GitHub credential from .dev-credentials.yaml

```bash
ALCOVE_TOKEN="$TOKEN" python3 -c "
import yaml, json, subprocess, os, sys

token = os.environ['ALCOVE_TOKEN']

creds_file = '.dev-credentials.yaml'
if not os.path.exists(creds_file):
    print('SKIP: .dev-credentials.yaml not found. No GitHub credential created.')
    print('Falling back to gh auth token...')
    r = subprocess.run(['gh', 'auth', 'token'], capture_output=True, text=True)
    gh_token = r.stdout.strip() if r.returncode == 0 else ''
    if not gh_token:
        print('SKIP: No GitHub token available. Agent repo sync will not work.')
        sys.exit(0)
else:
    with open(creds_file) as f:
        creds = yaml.safe_load(f) or {}
    gh_token = creds.get('github_token', '')
    if not gh_token:
        print('No github_token in .dev-credentials.yaml, falling back to gh auth token...')
        r = subprocess.run(['gh', 'auth', 'token'], capture_output=True, text=True)
        gh_token = r.stdout.strip() if r.returncode == 0 else ''
    if not gh_token:
        print('SKIP: No GitHub token available. Agent repo sync will not work.')
        sys.exit(0)

payload = json.dumps({
    'name': 'github',
    'provider': 'github',
    'auth_type': 'api_key',
    'credential': gh_token,
})

r = subprocess.run(['curl', '-s', '-X', 'POST',
    'http://localhost:8080/api/v1/credentials',
    '-H', f'Authorization: Bearer {token}',
    '-H', 'Content-Type: application/json',
    '-d', payload], capture_output=True, text=True)
print(r.stdout)
if r.returncode != 0:
    print(f'curl error: {r.stderr}', file=sys.stderr)
    sys.exit(1)
"
```

Verify it was created (should print JSON with an `id` field, not an error).

## Step 7: Configure bmbouter/alcove-testing agent repo

```bash
curl -s -X PUT http://localhost:8080/api/v1/user/settings/agent-repos \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"repos":[{"url":"https://github.com/bmbouter/alcove-testing.git","ref":"main","name":"alcove-testing"}]}'
```

Trigger sync:
```bash
curl -s -X POST http://localhost:8080/api/v1/agent-definitions/sync -H "Authorization: Bearer $TOKEN"
```

Verify sync succeeded:
```bash
podman logs alcove-bridge 2>&1 | grep "agent-repo-syncer: synced"
```

Must show "synced N agent definition(s)" with N > 0. If it shows 0 or no output, the repo URL or GitHub credential may be wrong.

## Step 8: Final verification

Confirm credentials are configured:
```bash
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/credentials
```
Must show at least 1 credential (GitHub). If system_llm was configured, should show 2 credentials (LLM + GitHub).

```bash
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/sessions?per_page=1
```
Must show `"total": 0` (clean database).

## Postgres auth backend

If postgres auth is needed instead of the default memory backend, restart Bridge with `AUTH_BACKEND=postgres` using the manual container command from Step 3.

## Summary

Three things configured:
1. LLM credential (from .dev-credentials.yaml llm section -- supports anthropic, claude-oauth, or google-vertex)
2. GitHub PAT (from .dev-credentials.yaml github_token, falling back to gh auth token)
3. bmbouter/alcove-testing agent repo (synced)

One thing verified:
- Agent repo sync shows N > 0 definitions
