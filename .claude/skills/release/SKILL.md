---
name: release
description: Trigger the automated release pipeline for Alcove
user-invocable: true
argument-hint: "[version] (optional — omit to let the release agent decide)"
---

Trigger the Alcove automated release pipeline. Releases are handled by the Automated Release Agent task running on the staging environment — not locally.

## How It Works

The release agent (`.alcove/tasks/release.yml`) runs daily at 6 AM UTC and checks for unreleased commits. To trigger an immediate release:

## Steps

### 1. Create or find an issue to tag
If there's already an open issue, use it. Otherwise create one:
```bash
gh issue create --repo bmbouter/alcove --title "Release vX.Y.Z" --body "Triggering immediate release."
```

### 2. Add the immediate-release label
```bash
gh issue edit ISSUE_NUMBER --repo bmbouter/alcove --add-label "immediate-release"
```

The release agent will:
1. Check for unreleased commits on main
2. Determine the semver bump (features → minor, fixes → patch)
3. Generate changelog in `docs/changelog.md`
4. Open a PR, wait for CI to pass, merge
5. Tag and push the version
6. Monitor the GitHub Actions release build (builds binaries + container images on ghcr.io)

### 3. Monitor progress
Watch for the release agent task to appear in the Alcove staging dashboard. It will show up as a running task within ~60 seconds of the label being added.

### 4. Verify
After the release agent completes:
```bash
gh release view --repo bmbouter/alcove
```

## Notes
- The release agent runs on staging, not locally
- It uses the `alcove-developer` security profile for full repo access
- Building and publishing containers happens in GitHub Actions (triggered by tag push)
- Do NOT manually tag or push — the release agent handles everything
