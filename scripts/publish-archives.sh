#!/usr/bin/env bash
set -euo pipefail

tag=${1:?Usage: publish-archives.sh EXISTING_TAG DIST_DIRECTORY}
dist_dir=${2:?Supply the reviewed artifact directory}
repo=akopichin/afm
manifest="$dist_dir/build-manifest.json"
test -f "$manifest"
python3 - "$manifest" "$tag" <<'PY'
import hashlib, json, pathlib, subprocess, sys
path = pathlib.Path(sys.argv[1])
m = json.loads(path.read_text())
commit = subprocess.check_output(['git','rev-parse',f'refs/tags/{sys.argv[2]}^{{commit}}'],text=True).strip()
assert commit == m['afm_commit'], 'Tag differs from reviewed product commit'
for name, sha in m['artifacts_sha256'].items():
    assert hashlib.sha256((path.parent/name).read_bytes()).hexdigest() == sha, name
PY
latest=false
prerelease=false
case "$tag" in
  *-*) prerelease=true ;;
  v1.*|v2.*) ;;
  *) latest=true ;;
esac
if ! gh release view "$tag" --repo "$repo" >/dev/null 2>&1; then
  gh release create "$tag" --repo "$repo" --verify-tag --draft --title "$tag" --notes "See CHANGELOG.md and the build manifest."
fi
# Existing assets are immutable: retry only uploads missing names.
for artifact in "$dist_dir"/*.tar.gz "$dist_dir"/*.zip "$dist_dir/checksums.txt" "$manifest"; do
  test -f "$artifact" || continue
  name=$(basename "$artifact")
  if gh release view "$tag" --repo "$repo" --json assets --jq '.assets[].name' | grep -Fxq "$name"; then
    download=$(mktemp -d)
    gh release download "$tag" --repo "$repo" --pattern "$name" --dir "$download"
    cmp "$artifact" "$download/$name"
    find "$download" -type f -delete
    rmdir "$download"
  else
    gh release upload "$tag" "$artifact" --repo "$repo"
  fi
done
gh release edit "$tag" --repo "$repo" --draft=false --prerelease="$prerelease" --latest="$latest"
