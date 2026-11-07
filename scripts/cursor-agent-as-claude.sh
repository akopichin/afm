#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
exec bash "$root/contracts/adapters/cursor-agent-as-claude.sh" "$@"
