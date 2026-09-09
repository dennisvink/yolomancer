#!/usr/bin/env bash
set -euo pipefail

: "${GH_REPO:?}"
: "${GITHUB_SHA:?}"
: "${DEFAULT_BRANCH:?}"

# An old rerun must never move latest backwards past the default branch tip.
tip="$(gh api "repos/$GH_REPO/commits/$DEFAULT_BRANCH" --jq '.sha')"
if [[ "$tip" != "$GITHUB_SHA" ]]; then
  echo 'Skipping publication: this commit is no longer the default branch tip.'
  exit 0
fi

assets=(
  yolomancer-darwin-amd64 yolomancer-darwin-arm64
  yolomancer-linux-amd64 yolomancer-linux-arm64
  yolomancer-windows-amd64.exe yolomancer-windows-arm64.exe
)
cd dist
for asset in "${assets[@]}"; do
  test -s "$asset"
done
sha256sum "${assets[@]}" > SHA256SUMS

release_id="$(gh api "repos/$GH_REPO/releases" --paginate --jq '.[] | select(.tag_name == "latest") | .id')"
if [[ -n "$release_id" ]]; then
  immutable="$(gh api "repos/$GH_REPO/releases/$release_id" --jq '.immutable // false')"
  if [[ "$immutable" == true ]]; then
    echo 'The latest release is immutable; disable release immutability for this rolling-release workflow.' >&2
    exit 1
  fi
fi

tag_count="$(gh api "repos/$GH_REPO/git/matching-refs/tags/latest" --jq '[.[] | select(.ref == "refs/tags/latest")] | length')"
if [[ "$tag_count" == 0 ]]; then
  gh api --method POST "repos/$GH_REPO/git/refs" -f ref=refs/tags/latest -f sha="$GITHUB_SHA" --silent
else
  gh api --method PATCH "repos/$GH_REPO/git/refs/tags/latest" -f sha="$GITHUB_SHA" -F force=true --silent
fi

notes="Automated multi-platform build from commit $GITHUB_SHA. Download the binary for your OS/CPU and verify it against SHA256SUMS. No changelog-based versioning."
if [[ -z "$release_id" ]]; then
  gh release create latest --verify-tag --title Latest --notes "$notes" --latest
else
  gh release edit latest --title Latest --notes "$notes" --draft=false --prerelease=false --latest
fi
# Replace only these known assets; leave unrelated release assets alone.
gh release upload latest "${assets[@]}" --clobber
gh release upload latest SHA256SUMS --clobber
