#!/bin/sh
# Builds Mindaeon's Gitea image from a commit of this branch, reproducibly, and with --push
# publishes it and its SBOMs. The same script runs in CI (.github/workflows/mindaeon-image.yml)
# and on a workstation, so both build the same thing:
#
# - the context is a fresh shallow clone of the commit, never a working tree;
# - the base images are pinned by digest in Dockerfile.rootless;
# - the build arguments are fixed here, SOURCE_DATE_EPOCH is the commit's time, and BuildKit
#   rewrites every file and layer timestamp to it;
# - no attestations go into the image, so its digest is the image alone.
#
# Alpine's packages are installed at their current 3.24 versions, so a rebuild matches while
# Alpine's repository serves the same ones; the SBOMs record what went in.
#
# The image is built into a local OCI layout and checked before anything is pushed: when
# <image>:<version> or <image>:sha-<commit> exists with another digest, or MINDAEON_EXPECT_DIGEST
# names another digest, the script fails and pushes nothing; a version tag is never moved, so
# the change needs a new version in .mindaeon/version. Only then does --push publish the
# layout, byte for byte (crane), as sha-<commit> and then <version>.
#
#   .mindaeon/build-image.sh [--push] [<commit>]
#
# Needs docker buildx, crane and (with --push) syft on PATH. Environment: MINDAEON_IMAGE
# (ghcr.io/mindaeon/gitea), MINDAEON_PLATFORMS (linux/amd64,linux/arm64), MINDAEON_BUILDER
# (mindaeon-reproducible), MINDAEON_OUT (where the SBOMs, the OCI layout and the build's
# metadata are written; a new temporary folder by default), MINDAEON_EXPECT_DIGEST (the index
# digest this build must have, to prove a rebuild without pushing).
set -eu
# The clone below is the build's context: its files' modes must not depend on who runs this.
umask 022

push=false
if [ "${1:-}" = "--push" ]; then
  push=true
  shift
fi
repo=$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)
commit=$(git -C "$repo" rev-parse "${1:-HEAD}^{commit}")
image=${MINDAEON_IMAGE:-ghcr.io/mindaeon/gitea}
platforms=${MINDAEON_PLATFORMS:-linux/amd64,linux/arm64}
builder=${MINDAEON_BUILDER:-mindaeon-reproducible}
out=${MINDAEON_OUT:-$(mktemp -d)}
mkdir -p "$out"

context=$(mktemp -d)
trap 'rm -rf "$context"' EXIT
git -C "$context" init --quiet
git -C "$context" fetch --quiet --depth=1 "file://$repo" "$commit"
git -C "$context" -c advice.detachedHead=false checkout --quiet FETCH_HEAD

version=$(cat "$context/.mindaeon/version")
epoch=$(git -C "$context" log -1 --format=%ct)
# Gitea's own version is semver: ours is the release with build metadata, never a pre-release,
# so anything comparing versions sees 28.3.0. The image tag cannot carry "+".
gitea_version=$(echo "$version" | sed 's/-mindaeon\./+mindaeon./')

# The digest a tag names, nothing when the tag does not exist; any other failure (network,
# authentication) stops the script rather than reading as "not there".
lookup() {
  if crane digest "$1" 2>"$out/lookup.err"; then
    return 0
  elif grep -qE 'MANIFEST_UNKNOWN|NAME_UNKNOWN|404' "$out/lookup.err"; then
    return 0
  fi
  cat "$out/lookup.err" >&2
  return 1
}

# Checks the digest against everything it must match. Run before the build to fail early and
# again after it, since a build takes a while; nothing is pushed until it passes.
check() {
  expect=${MINDAEON_EXPECT_DIGEST:-}
  version_digest=$(lookup "$image:$version")
  sha_digest=$(lookup "$image:sha-$commit")
  [ -n "${1:-}" ] || return 0
  if [ -n "$expect" ] && [ "$expect" != "$1" ]; then
    echo "this build is $1, MINDAEON_EXPECT_DIGEST is $expect: not reproduced" >&2
    return 1
  fi
  if [ -n "$version_digest" ] && [ "$version_digest" != "$1" ]; then
    echo "$image:$version is $version_digest, this build is $1: give the change a new version in .mindaeon/version" >&2
    return 1
  fi
  if [ -n "$sha_digest" ] && [ "$sha_digest" != "$1" ]; then
    echo "$image:sha-$commit is $sha_digest, this build is $1: not reproduced" >&2
    return 1
  fi
}

check ""
layout="$out/oci"
rm -rf "$layout"
echo "building $image:$version from $commit ($platforms)" >&2
docker buildx build --builder "$builder" --no-cache \
  --file "$context/Dockerfile.rootless" --target gitea-rootless \
  --platform "$platforms" \
  --build-arg GITEA_VERSION="$gitea_version" \
  --build-arg SOURCE_DATE_EPOCH="$epoch" \
  --provenance=false --sbom=false \
  --annotation "index:org.opencontainers.image.source=https://github.com/mindaeon/gitea" \
  --annotation "index:org.opencontainers.image.revision=$commit" \
  --annotation "index:org.opencontainers.image.version=$version" \
  --annotation "index:org.opencontainers.image.licenses=MIT" \
  --metadata-file "$out/metadata.json" \
  --output "type=oci,tar=false,dest=$layout,name=$image:$version,rewrite-timestamp=true,oci-mediatypes=true" \
  "$context"

digest=$(sed -n 's/.*"containerimage.digest": *"\(sha256:[0-9a-f]*\)".*/\1/p' "$out/metadata.json")
if [ -z "$digest" ] || ! grep -q "\"digest\":\"$digest\"" "$layout/index.json"; then
  echo "the build's metadata and its OCI layout do not name one image index" >&2
  exit 1
fi
echo "$image@$digest" >"$out/image"
echo "built $image:$version@$digest" >&2
check "$digest"
if [ -n "$version_digest" ]; then
  echo "$image:$version is already this build: reproduced" >&2
elif [ -n "${MINDAEON_EXPECT_DIGEST:-}" ]; then
  echo "this build is MINDAEON_EXPECT_DIGEST: reproduced" >&2
fi
[ "$push" = true ] || exit 0

# Push the layout itself, so the registry holds exactly the bytes that were checked.
publish() {
  pushed=$(lookup "$image:$1")
  if [ -z "$pushed" ]; then
    crane push "$layout" "$image:$1" >&2
    pushed=$(lookup "$image:$1")
  fi
  if [ "$pushed" != "$digest" ]; then
    echo "$image:$1 is $pushed after the push, this build is $digest" >&2
    exit 1
  fi
}
publish "sha-$commit"
publish "$version"

for platform in $(echo "$platforms" | tr ',' ' '); do
  arch=$(echo "$platform" | tr '/' '-')
  syft scan "registry:$image@$digest" --platform "$platform" \
    --source-name "$image" --source-version "$version" \
    -o "spdx-json=$out/gitea-$version-$arch.spdx.json"
done
echo "$image:$version@$digest" >"$out/image"
echo "published $image:$version@$digest; SBOMs in $out" >&2
