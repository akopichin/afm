#!/usr/bin/env bash
# Test: codex-as-claude.sh injects the afm_chrome MCP -c overrides for a normal author pass and NOT for a
# verify pass.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
adapter="$here/codex-as-claude.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

argv_file="$tmp/argv"
fake_codex="$tmp/fakecodex"
cat >"$fake_codex" <<'EOF'
#!/usr/bin/env bash
# --help probe: do NOT advertise --output-last-message.
for a in "$@"; do
  if [ "$a" = "--help" ]; then echo "usage: codex exec"; exit 0; fi
done
# Real exec: record argv, emit a minimal valid stream, exit 0.
printf '%s\n' "$*" >"$ARGV_FILE"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"done"}}'
printf '%s\n' '{"type":"turn.completed","usage":{}}'
exit 0
EOF
chmod +x "$fake_codex"

# A PATH WITHOUT any `afm` binary: a bare-`afm` MCP command would fail, so the
# adapter must use the exported absolute launcher path, not the literal "afm".
clean_path="$tmp/bin"
mkdir -p "$clean_path"
ln -s /usr/bin/jq "$clean_path/jq"

fail() { echo "FAIL: $1" >&2; exit 1; }

# --- Case 1: normal author pass → overrides present ---
: >"$argv_file"
ARGV_FILE="$argv_file" CODEX_BIN="$fake_codex" \
  AFM_BROWSER_DESCRIPTOR="$tmp/desc.json" \
  AFM_BROWSER_LAUNCHER="/opt/afm/bin/afm" \
  PATH="$clean_path:/usr/bin:/bin" \
  bash "$adapter" -p "hello" >/dev/null 2>&1 || true

argv="$(cat "$argv_file" 2>/dev/null || true)"
case "$argv" in
  *'mcp_servers.afm_chrome.command="/opt/afm/bin/afm"'*) : ;;
  *) fail "expected afm_chrome.command override with the launcher path; got: $argv" ;;
esac
case "$argv" in
  *'mcp_servers.afm_chrome.args=["browser","mcp","--descriptor","'"$tmp"'/desc.json"]'*) : ;;
  *) fail "expected afm_chrome.args sequence override; got: $argv" ;;
esac
case "$argv" in
  *' afm '*|*'"afm"'*) fail "bare 'afm' must not appear as the MCP command; got: $argv" ;;
esac

# --- Case 2: verify pass → overrides absent ---
: >"$argv_file"
ARGV_FILE="$argv_file" CODEX_BIN="$fake_codex" CODEX_VERIFY=1 \
  AFM_BROWSER_DESCRIPTOR="$tmp/desc.json" \
  AFM_BROWSER_LAUNCHER="/opt/afm/bin/afm" \
  PATH="$clean_path:/usr/bin:/bin" \
  bash "$adapter" -p "hello" >/dev/null 2>&1 || true

argv="$(cat "$argv_file" 2>/dev/null || true)"
case "$argv" in
  *'afm_chrome'*) fail "verify pass must NOT inject afm_chrome overrides; got: $argv" ;;
esac

echo "PASS: codex-as-claude browser overrides"
