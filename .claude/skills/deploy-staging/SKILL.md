---
name: deploy-staging
description: Check for new releases, security audit changes, and deploy to OpenShift staging
user-invocable: true
argument-hint: "[version] (optional — omit to auto-detect from latest release vs deployed)"
---

Automatically detect if a new Alcove release is available, audit the changes for safety, and deploy to staging if approved.

## Steps

### 1. Determine versions

**Latest release:**
```bash
gh release view --repo bmbouter/alcove --json tagName -q '.tagName'
```

**Currently deployed on staging:**
```bash
oc get deployment alcove-bridge -o jsonpath='{.spec.template.spec.containers[0].image}'
```
Extract the tag from the image URL (e.g., `ghcr.io/bmbouter/alcove-bridge:0.4.15` → `0.4.15`).

If $ARGUMENTS is provided, use that version instead of auto-detecting.

**Compare:** If the deployed version matches the latest release, report "staging is up to date" and exit. If oc login is expired, ask the user for a fresh token.

### 2. Audit changes for safety

Read the changelog and commit diff between the deployed version and the target version:
```bash
git log v{deployed}..v{target} --oneline
```

Read `docs/changelog.md` for the target version's entry.

**Spawn a team of agents in parallel to audit the changes:**

- **Architecture agent**: Review for breaking API changes, database migration risks, backward compatibility issues. Check if new migrations exist and whether they are additive (safe) or destructive.

- **Security agent**: Check for new credential handling, auth changes, scope changes, network policy modifications. Flag any changes to Gate proxy, credential encryption, or token handling. Verify no secrets are exposed in new code.

- **Staging compatibility agent**: Check for new environment variables, template parameters, or config changes needed in `deploy/openshift/template.yaml` or `data/services/pulp/deploy-alcove.yml`. Flag anything that requires manual app-interface config changes beyond image tag bumps.

- **Rollback risk agent**: Assess whether the deployment can be safely rolled back by reverting the image tag. Flag any one-way migrations or state changes that would prevent rollback.

Each agent should report: SAFE, CAUTION (proceed with noted risks), or BLOCK (do not deploy without resolution).

### 3. Decision

- If all agents report SAFE: proceed to deploy automatically
- If any agent reports CAUTION: present the findings to the user and ask whether to proceed
- If any agent reports BLOCK: stop and report the blocking issue to the user. Do not proceed without explicit approval.

If config changes are needed beyond image tags and the context doesn't tell you what values to use, ASK THE USER.

### 4. Deploy via app-interface MR

```bash
cd ~/devel/pulp/app-interface
git checkout master && git pull origin master
git checkout -b alcove-vXYZ master
```

Edit `data/services/pulp/deploy-alcove.yml` — update BRIDGE_IMAGE_TAG, GATE_IMAGE, SKIFF_IMAGE tags. Add any new parameters identified by the staging compatibility agent.

```bash
git add -A && git commit -m "Upgrade Alcove to vX.Y.Z — summary"
git push fork BRANCH_NAME
```

### 5. Open the MR via GitLab API

```bash
GITLAB_TOKEN=$(cat ~/.config/alcove-gitlab-token)
curl -s -X POST "https://gitlab.cee.redhat.com/api/v4/projects/79823/merge_requests" \
  -H "PRIVATE-TOKEN: $GITLAB_TOKEN" -H "Content-Type: application/json" \
  -d '{"source_branch":"BRANCH","target_branch":"master","target_project_id":13582,"title":"TITLE","description":"DESC including audit findings"}'
```

### 6. Wait for pipeline, then post /lgtm

Poll the pipeline status every 60 seconds. Once it passes:
```bash
curl -s -X POST "https://gitlab.cee.redhat.com/api/v4/projects/13582/merge_requests/MR_IID/notes" \
  -H "PRIVATE-TOKEN: $GITLAB_TOKEN" -H "Content-Type: application/json" \
  -d '{"body": "/lgtm"}'
```

### 7. Monitor deployment

Watch for the new image version to appear:
```bash
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  sleep 30
  IMG=$(oc get deployment alcove-bridge -o jsonpath='{.spec.template.spec.containers[0].image}')
  if echo "$IMG" | grep -q "TARGET_VERSION"; then echo "Deployed!"; break; fi
done
```

Report when live with the version number.
