#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
cd "$root"
# Send only application inputs; dependencies and packaged binaries belong to the container platform.
COPYFILE_DISABLE=1 tar -cf - desktop/package.json desktop/package-lock.json desktop/src desktop/renderer desktop/scripts desktop/test \
  pkg/web/dashboard/skins/base pkg/web/dashboard/skins/graphite pkg/web/dashboard/assets \
  pkg/web/dashboard/index.html pkg/web/dashboard/favicon.svg pkg/web/dashboard/chat-composer.css pkg/web/dashboard/side-agent.css |
  docker run --rm -i --entrypoint sh "${AFM_DESKTOP_CHECK_IMAGE:-akopichin/afm:v3.1.11}" -ec '
    root=$(mktemp -d)
    cd "$root"
    tar -xf -
    cd desktop
    npm ci --no-audit --no-fund
    npm run check
    npm test
    npm run perf
    npm run build
    node scripts/check-package.js
  '
