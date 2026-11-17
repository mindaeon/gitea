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
# A version tag is never moved: when <image>:<version> exists with another digest, the push
# fails, and the change needs a new version in .mindaeon/version.
#
#   .mindaeon/build-image.sh [--push] [<commit>]
#
# Environment: MINDAEON_IMAGE (ghcr.io/mindaeon/gitea), MINDAEON_PLATFORMS
# (linux/amd64,linux/arm64), MINDAEON_BUILDER (mindaeon-reproducible), MINDAEON_OUT (where the
# SBOMs and the build's metadata are written; a new temporary folder by default).
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
  --output "type=image,name=$image:sha-$commit,push=$push,rewrite-timestamp=true,oci-mediatypes=true" \
  "$context"

digest=$(sed -n 's/.*"containerimage.digest": *"\(sha256:[0-9a-f]*\)".*/\1/p' "$out/metadata.json")
echo "$image@$digest" >"$out/image"
echo "built $image:sha-$commit@$digest" >&2
[ "$push" = true ] || exit 0

existing=$(docker buildx imagetools inspect "$image:$version" --format '{{.Manifest.Digest}}' 2>/dev/null || true)
if [ -z "$existing" ]; then
  docker buildx imagetools create --tag "$image:$version" "$image@$digest"
elif [ "$existing" != "$digest" ]; then
  echo "$image:$version is $existing, this build is $digest: give the change a new version in .mindaeon/version" >&2
  exit 1
else
  echo "$image:$version is already this build: reproduced" >&2
fi

for platform in $(echo "$platforms" | tr ',' ' '); do
  arch=$(echo "$platform" | tr '/' '-')
  syft scan "registry:$image@$digest" --platform "$platform" \
    --source-name "$image" --source-version "$version" \
    -o "spdx-json=$out/gitea-$version-$arch.spdx.json"
done
echo "$image:$version@$digest" >"$out/image"
echo "published $image:$version@$digest; SBOMs in $out" >&2
