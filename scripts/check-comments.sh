#!/usr/bin/env bash
# Проверяет добавленные относительно BASE (по умолчанию HEAD) и неотслеживаемые
# Go/TS-комментарии: блок не длиннее MAX_LINES строк, без номеров задач плана.

set -Eeuo pipefail

readonly BASE=${1:-HEAD}
readonly MAX_LINES=${MAX_COMMENT_LINES:-3}
readonly FILES_RE='\.(go|ts|tsx)$'
readonly SKIP_RE='(^|/)(vendor|node_modules)/|(^|/)doc\.go$|\.gen\.ts$'

# added_diff печатает -U0 diff изменённых и новых файлов, как будто всё добавлено.
added_diff() {
  git diff -U0 --no-color --diff-filter=AM "$BASE" -- . ':!vendor'
  git ls-files --others --exclude-standard | while read -r f; do
    git diff -U0 --no-color --no-index /dev/null "$f" || true
  done
}

added_diff | awk -v max="$MAX_LINES" -v files_re="$FILES_RE" -v skip_re="$SKIP_RE" '
function flush() {
  if (run > max) { printf "%s:%d: блок комментария из %d строк (максимум %d)\n", file, start, run, max; bad = 1 }
  run = 0
}
/^\+\+\+ / { flush(); file = substr($0, 7); sub(/^b\//, "", file); check = file ~ files_re && file !~ skip_re; next }
/^@@/      { flush(); split($3, h, ","); line = substr(h[1], 2) + 0; next }
!check     { next }
/^\+/ {
  text = substr($0, 2)
  if (text ~ /^[ \t]*\/\// && text !~ /^[ \t]*\/\/(go:|nolint|eslint|@ts-)/) {
    if (run == 0) start = line
    run++
    if (text ~ /(^|[^A-Za-z0-9])T[0-9]+\.[0-9]+|Phase [0-9]/) {
      printf "%s:%d: номер задачи/фазы плана в комментарии\n", file, line; bad = 1
    }
  } else flush()
  line++
  next
}
{ flush() }
END {
  flush()
  if (bad) {
    print "Комментарии: 1–2 строки сути (почему, а не что); история и номера задач — в коммит или docs/superpowers/." > "/dev/stderr"
    exit 1
  }
}'
