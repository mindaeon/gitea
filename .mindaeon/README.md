# Mindaeon's Gitea

This branch is Gitea's release tag plus the changes Mindaeon ships, one commit each, with the
reason in its message (`git log v28.3.0..`). Mindaeon's platform pins the image built from it
by digest; how the fork is kept is in the platform's `docs/operations/forked-components.md`.

- `.mindaeon/version`: the version of the next image, `<upstream>-mindaeon.<n>`. Raise `<n>`
  with every change to the branch: a published version is never rebuilt differently.
- `.mindaeon/build-image.sh [--push]`: the reproducible build, run by
  `.github/workflows/mindaeon-image.yml` on every push to a `mindaeon/*` branch.
- Gitea reports its version as `<upstream>+mindaeon.<n>` (semver build metadata).
