#!/usr/bin/env bash
set -euo pipefail

afm_root=$(cd "$(dirname "$0")/.." && pwd)
engine_root=$(cd "${1:?Usage: dev-workspace.sh /path/to/afm-engine}" && pwd)
test -f "$engine_root/go.mod"
cd "$afm_root"
if [ -e go.work ]; then
  echo "go.work already exists; update it with go work use instead" >&2
  exit 1
fi
GOWORK=off go work init . ./contracts "$engine_root" "$engine_root/examples/minimal" "$engine_root/integration/afm"
module_version() {
  awk -v module="$1" '$1 == module { print $2 } $1 == "require" && $2 == module { print $3 }' go.mod
}
go work edit -replace "github.com/akopichin/afm-engine@$(module_version github.com/akopichin/afm-engine)=$engine_root"
go work edit -replace "github.com/akopichin/afm/contracts@$(module_version github.com/akopichin/afm/contracts)=$afm_root/contracts"
echo "Local workspace created. Release checks must also pass with GOWORK=off."
