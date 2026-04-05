---
name: deploy-staging
description: Deploy a new Alcove version to the OpenShift staging environment via app-interface MR
user-invocable: true
argument-hint: "[version] (e.g., 0.3.15 — omit to use latest release)"
---

Deploy Alcove to the OpenShift staging environment (pulp-stage on crcs02ue1).

## Steps

### 1. Determine version
If $ARGUMENTS is provided, use it. Otherwise, find the latest release:
```bash
gh release view --repo bmbouter/alcove --json tagName -q '.tagName'
```

### 2. Check for config changes
Read `docs/changelog.md` for the target version. Look for new template parameters, auth changes, new secrets. If config changes are needed and the context doesn't tell you what values to use, ASK THE USER.

### 3. Create the app-interface branch
```bash
cd ~/devel/pulp/app-interface
git checkout master && git pull origin master
git checkout -b alcove-vXYZ master
```

### 4. Update deploy-alcove.yml
Edit `data/services/pulp/deploy-alcove.yml` — update BRIDGE_IMAGE_TAG, GATE_IMAGE, SKIFF_IMAGE tags. Add any new parameters.

### 5. Commit and push
```bash
git add -A && git commit -m "Upgrade Alcove to vX.Y.Z — summary"
git push fork BRANCH_NAME
```

### 6. Open the MR via GitLab API
```bash
GITLAB_TOKEN=$(cat ~/.config/alcove-gitlab-token)
curl -s -X POST "https://gitlab.cee.redhat.com/api/v4/projects/79823/merge_requests" \
  -H "PRIVATE-TOKEN: $GITLAB_TOKEN" -H "Content-Type: application/json" \
  -d '{"source_branch":"BRANCH","target_branch":"master","target_project_id":13582,"title":"TITLE","description":"DESC"}'
```

### 7. Post /lgtm
```bash
curl -s -X POST "https://gitlab.cee.redhat.com/api/v4/projects/13582/merge_requests/MR_IID/notes" \
  -H "PRIVATE-TOKEN: $GITLAB_TOKEN" -H "Content-Type: application/json" \
  -d '{"body": "/lgtm"}'
```

### 8. Monitor deployment via oc
Watch for the new image version to appear on the Bridge deployment. Report when live.
