#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
exec bash "$root/contracts/adapters/openai-agent-as-claude.sh" "$@"
