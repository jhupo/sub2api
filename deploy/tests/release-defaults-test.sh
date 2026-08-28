#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

assert_contains() {
  file=$1
  expected=$2
  if ! grep -Fq "$expected" "$file"; then
    printf '%s must contain: %s\n' "$file" "$expected" >&2
    exit 1
  fi
}

assert_not_contains() {
  file=$1
  unexpected=$2
  if grep -Fq "$unexpected" "$file"; then
    printf '%s must not contain: %s\n' "$file" "$unexpected" >&2
    exit 1
  fi
}

assert_contains backend/internal/service/update_service.go 'githubRepo     = "jhupo/sub2api"'
assert_contains frontend/src/components/common/VersionBadge.vue "const GITHUB_REPO = 'jhupo/sub2api'"
assert_contains frontend/src/components/common/VersionBadge.vue "const DOCKER_IMAGE = 'ghcr.io/jhupo/sub2api'"
assert_contains deploy/install.sh 'GITHUB_REPO="jhupo/sub2api"'
assert_contains deploy/update-orchestrator.sh 'REPOSITORY="${SUB2API_UPDATE_REPOSITORY:-jhupo/sub2api}"'

for compose_file in \
  deploy/docker-compose.yml \
  deploy/docker-compose.local.yml \
  deploy/docker-compose.standalone.yml
do
  assert_contains "$compose_file" 'image: ghcr.io/jhupo/sub2api:${SUB2API_VERSION:-latest}'
  assert_contains "$compose_file" 'SUB2API_UPDATE_REPOSITORY=${SUB2API_UPDATE_REPOSITORY:-jhupo/sub2api}'
done

assert_contains .github/workflows/release.yml "      - 'v*'"
assert_contains .github/workflows/release.yml "RELEASE_TAG: \${{ github.event.inputs.tag || github.ref_name }}"
assert_contains .github/workflows/release.yml '  cancel-in-progress: false'
assert_contains .github/workflows/release.yml '  packages: write'
assert_contains .github/workflows/release.yml '      - name: Login to GitHub Container Registry'
assert_not_contains .github/workflows/release.yml '  create:'
assert_not_contains .github/workflows/release.yml 'PUBLISH_GHCR'
assert_not_contains .goreleaser.yaml '.Env.PUBLISH_GHCR'
assert_not_contains .goreleaser.simple.yaml '.Env.PUBLISH_GHCR'

printf 'release defaults test passed\n'
