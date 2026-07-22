#!/bin/sh
# Бампит SemVer от последнего git-тега и пушит новый тег vX.Y.Z.
# Сама сборка (docker-образ, бинарники, Homebrew formula) происходит в
# GitHub Actions (.github/workflows/release.yml), которая реагирует на пуш
# этого тага — единая точка входа в релиз что для авто-патча из CI (push в
# main), что для ручного minor/major отсюда.
# --dry-run: только напечатать следующую версию (без git tag/push).
set -e

if [ "$1" = "--dry-run" ]; then dry=1; shift; else dry=0; fi
level="$1"
case "$level" in
    patch|minor|major) ;;
    *) echo "usage: $0 [--dry-run] {patch|minor|major}" >&2; exit 2 ;;
esac

# последний SemVer-тег v[0-9]* (несемверные/экспериментальные игнорируются), или v0.0.0
latest=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
[ -n "$latest" ] || latest=v0.0.0
latest=${latest#v}
major=$(echo "$latest" | cut -d. -f1)
minor=$(echo "$latest" | cut -d. -f2)
patch=$(echo "$latest" | cut -d. -f3)

case "$level" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
esac
next="v$major.$minor.$patch"

if [ "$dry" = 1 ]; then
    echo "$next"
    exit 0
fi

# не релизить грязное состояние
if [ -n "$(git status --porcelain)" ]; then
    echo "error: working tree not clean — commit/stash first" >&2
    exit 1
fi
# предохранитель от повторного тега
if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
    echo "error: tag $next already exists" >&2
    exit 1
fi

echo "tagging $next (latest was ${latest:-none})"
git tag -a "$next" -m "Release $next"
git push origin "$next"
echo "tagged and pushed $next — release.yml will build+publish it"
