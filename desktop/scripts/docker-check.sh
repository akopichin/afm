#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
cd "$root"
image=${AFM_DESKTOP_CHECK_IMAGE:-akopichin/afm:v3.1.11}
arch=$(docker run --rm --entrypoint node "$image" -p process.arch)
case "$arch" in
  arm64) goarch=arm64 ;;
  x64) goarch=amd64 ;;
  *) echo "Unsupported Docker architecture: $arch" >&2; exit 1 ;;
esac
sidecar="desktop/dist/sidecars/linux-$arch/afm"
mkdir -p "$(dirname "$sidecar")"
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -trimpath -ldflags "-X main.version=0.0.1-desktop.$(git rev-parse --short HEAD)" -o "$sidecar" ./cmd/afm
set -- "$sidecar"
for zip in desktop/dist/electron-zip/electron-*-linux-"$arch".zip; do
  if [ -f "$zip" ]; then set -- "$@" "$zip"; fi
done
# Only the native sidecar and cached Electron zip cross the platform boundary.
COPYFILE_DISABLE=1 tar -cf - "$@" desktop/package.json desktop/package-lock.json desktop/assets desktop/src desktop/renderer desktop/scripts desktop/test \
  pkg/web/dashboard/skins/base pkg/web/dashboard/skins/graphite pkg/web/dashboard/assets \
  pkg/web/dashboard/index.html pkg/web/dashboard/favicon.svg pkg/web/dashboard/chat-composer.css pkg/web/dashboard/side-agent.css |
  docker run --rm --init -i --entrypoint sh "$image" -ec '
    root=$(mktemp -d)
    cd "$root"
    tar -xf -
    cd desktop
    ELECTRON_SKIP_BINARY_DOWNLOAD=1 npm ci --no-audit --no-fund
    npm run check
    npm test
    npm run perf
    npm run build
    node scripts/check-package.js
  '
