---
name: release
description: Create a new Alcove release — changelog, tag, build, publish, and verify containers
user-invocable: true
argument-hint: "[version] (e.g., 0.4.0 — omit to auto-detect from semver)"
---

Create a new Alcove release. Steps:

1. **Determine version**: If $ARGUMENTS is provided, use it. Otherwise, examine commits since the last tag (`git log $(git describe --tags --abbrev=0)..HEAD --oneline`) and determine the semver bump:
   - New features → minor bump (0.x.0)
   - Bug fixes only → patch bump (0.0.x)

2. **Generate changelog**: Read `docs/changelog.md` and the commit log since the last release. Add a new `## vX.Y.Z` section at the top with categorized entries (features, bug fixes, breaking changes). Follow the existing changelog format exactly.

3. **Commit the changelog**: `git add docs/changelog.md && git commit` with message "Add vX.Y.Z changelog"

4. **Push to main**: `git push origin main`

5. **Tag and push**: Create an annotated tag with a summary of highlights:
   ```
   git tag -a vX.Y.Z -m "Alcove vX.Y.Z — <one-line summary>"
   git push origin vX.Y.Z
   ```

6. **Monitor the release build**: The tag push triggers the GitHub Actions Release workflow. Monitor with:
   ```
   gh run list --repo bmbouter/alcove --limit 1
   ```
   Wait for completion (check every 15 seconds).

7. **Verify release artifacts**:
   - `gh release view vX.Y.Z --repo bmbouter/alcove` — check assets
   - Pull all 3 container images from ghcr.io to verify they exist:
     ```
     for img in alcove-bridge alcove-gate alcove-skiff-base; do
       podman pull ghcr.io/bmbouter/$img:X.Y.Z
     done
     ```

8. **Report**: Print the release URL, asset list, and image pull confirmation.
