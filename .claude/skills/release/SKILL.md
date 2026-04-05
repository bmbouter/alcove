---
name: release
description: Create a new Alcove release — changelog, tag, push, and verify CI builds containers
user-invocable: true
argument-hint: "[version] (e.g., 0.4.0 — omit to auto-detect from semver)"
---

Create a new Alcove release. All building happens in GitHub Actions, not locally.

## Steps

1. **Determine version**: If $ARGUMENTS is provided, use it. Otherwise, examine commits since the last tag and determine the semver bump (features → minor, fixes → patch).

2. **Generate changelog**: Add a new `## vX.Y.Z` section to `docs/changelog.md` with categorized entries from the commit log.

3. **Create a PR**: Commit the changelog, push to a branch, open a PR against main via `gh pr create`. Wait for CI to pass (all three jobs: unit-tests, build, functional-tests).

4. **Merge the PR**: Once CI passes, merge via `gh pr merge --squash`.

5. **Tag and push**: After merge, pull main, create an annotated tag, push it:
   ```bash
   git checkout main && git pull
   git tag -a vX.Y.Z -m "Alcove vX.Y.Z — summary"
   git push origin vX.Y.Z
   ```

6. **Monitor release build**: The tag push triggers the Release workflow in GitHub Actions. Monitor with `gh run list` until it completes successfully.

7. **Verify**: Check `gh release view vX.Y.Z` for assets and pull container images to confirm they exist on ghcr.io.
