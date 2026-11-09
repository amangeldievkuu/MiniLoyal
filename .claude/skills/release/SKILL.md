---
name: release
description: Cut a MiniLoyal release: changelog, version tag, container images and GitHub release. User-invoked only.
disable-model-invocation: true
argument-hint: "vX.Y.Z"
---

# Release

Pre-conditions: you are on `main`, the tree is clean, and `make ci` passes.

1. **Changelog.** Update `CHANGELOG.md` with a section for the version and date. Group entries as Added, Changed, Fixed, Security. Commit as `Prepare the vX.Y.Z changelog`.
2. **Version.** Update the version in `sdk/js/package.json` when the SDK changed. Commit as `Release vX.Y.Z`.
3. **Tag.** `git tag -a vX.Y.Z -m "vX.Y.Z"` and `git push origin main --tags`. Never force-push.
4. **Images.** `docker build --target api -t ghcr.io/amangeldievkuu/miniloyal-api:vX.Y.Z .` and `docker build --target worker -t ghcr.io/amangeldievkuu/miniloyal-worker:vX.Y.Z .`. Push both when registry access exists; otherwise report the commands.
5. **GitHub release.** `gh release create vX.Y.Z --notes-from-tag` or paste the changelog section.
6. **SDK.** If the SDK version changed, `npm publish --access public` from `sdk/js` after `npm run build`. Trusted publishing through GitHub Actions is preferred; report if it is not configured.
7. **Report.** Tag, image names, release URL, and anything skipped.
