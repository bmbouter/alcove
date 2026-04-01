---
name: deploy-staging
description: Deploy a new Alcove version to the OpenShift staging environment via app-interface MR
user-invocable: true
argument-hint: "[version] (e.g., 0.3.1 — omit to use latest release)"
---

Deploy Alcove to the OpenShift staging environment (pulp-stage on crcs02ue1).

## Prerequisites
- The app-interface repo is checked out at `~/devel/pulp/app-interface`
- The `fork` git remote points to the user's fork on gitlab.cee.redhat.com
- `oc` is logged into the crcs02ue1 cluster
- The version to deploy has been released (containers on ghcr.io)

## Steps

### 1. Determine version
If $ARGUMENTS is provided, use it. Otherwise, find the latest release:
```
gh release view --repo bmbouter/alcove --json tagName -q '.tagName'
```

### 2. Check for config changes
Read the changelog (`docs/changelog.md`) for the target version. Look for:
- New template parameters (env vars, config keys)
- Auth backend changes
- New secrets or vault entries needed
- Any breaking changes

If config changes are needed and the context doesn't tell you what values to use, **ASK THE USER** before proceeding.

### 3. Create the app-interface branch
```bash
cd ~/devel/pulp/app-interface
git checkout master && git pull origin master
git checkout -b alcove-vXYZ-upgrade master
```

### 4. Update deploy-alcove.yml
Edit `data/services/pulp/deploy-alcove.yml`:
- Update `BRIDGE_IMAGE_TAG` to the new version
- Update `GATE_IMAGE` tag
- Update `SKIFF_IMAGE` tag
- Add any new parameters identified in step 2

### 5. Update other files if needed
If the changelog indicates changes to:
- Secrets template (`resources/pulp-stage/alcove-config.secret.yaml`)
- Namespace config (`data/services/pulp/namespaces/pulp-stage.yml`)
- Shared resources (`data/services/pulp/namespaces/shared-resources/stage.yml`)

Make those changes too.

### 6. Commit and push
```bash
git add -A
git commit -m "Upgrade Alcove to vX.Y.Z — <summary>"
git push fork alcove-vXYZ-upgrade
```

### 7. Open the MR
Create a cross-project MR from the fork (project 79823) targeting upstream service/app-interface (project 13582) using the GitLab REST API:

```bash
GITLAB_TOKEN=$(cat ~/.config/alcove-gitlab-token)
MR_RESPONSE=$(curl -s -X POST "https://gitlab.cee.redhat.com/api/v4/projects/79823/merge_requests" \
  -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"source_branch\": \"BRANCH_NAME\",
    \"target_branch\": \"master\",
    \"target_project_id\": 13582,
    \"title\": \"TITLE\",
    \"description\": \"DESCRIPTION\"
  }")
```

Extract the MR IID and URL from the response. Report the URL to the user.

### 8. Leave /lgtm comment
Post a `/lgtm` comment on the MR to approve it:

```bash
GITLAB_TOKEN=$(cat ~/.config/alcove-gitlab-token)
MR_IID=<from step 7>
curl -s -X POST "https://gitlab.cee.redhat.com/api/v4/projects/13582/merge_requests/$MR_IID/notes" \
  -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"body": "/lgtm"}'
```

### 9. Monitor deployment
After the MR is merged, monitor the staging environment:
```bash
for i in $(seq 1 60); do
  IMAGE=$(oc get deployment alcove-bridge -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)
  if echo "$IMAGE" | grep -q "X.Y.Z"; then
    echo "DEPLOYED!"
    oc logs deployment/alcove-bridge --tail=15
    break
  fi
  sleep 30
done
```

### 10. Verify
Once deployed, check:
- `oc get pods -l app.kubernetes.io/name=bridge` — pod is Running
- `oc logs deployment/alcove-bridge` — no errors, correct version
- Report the deployment status to the user
