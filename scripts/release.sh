#!/bin/sh
# Бампит SemVer от последнего стабильного тега и пушит vX.Y.Z[-alpha.N|-beta.N].
# --dry-run: только напечатать следующую версию (без git tag/push).
set -e

usage() {
    echo "usage: $0 [--dry-run] {patch|minor|major} [alpha|beta]" >&2
    exit 2
}

if [ "${1:-}" = "--dry-run" ]; then dry=1; shift; else dry=0; fi
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
level="$1"
channel="${2:-}"
case "$level" in
    patch|minor|major) ;;
    *) usage ;;
esac
case "$channel" in
    ''|alpha|beta) ;;
    *) usage ;;
esac

# Только стабильные SemVer-теги, достижимые из HEAD: prerelease не меняет базу
# следующего бампа.
stable_tags=$(git tag --list --merged HEAD --sort=-version:refname)
latest=$(printf '%s\n' "$stable_tags" | awk '
    /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/ { print; exit }
')
[ -n "$latest" ] || latest=v0.0.0
version=${latest#v}
major=${version%%.*}
minor_patch=${version#*.}
minor=${minor_patch%%.*}
patch=${minor_patch#*.}

case "$level" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
esac
next="v$major.$minor.$patch"

if [ -n "$channel" ]; then
    # Счётчик отдельный для каждой версии и канала. Учитываем и теги на других
    # ветках: имя тега глобально для репозитория, его нельзя использовать снова.
    prerelease_tags=$(git tag --list "$next-$channel.*")
    number=$(printf '%s\n' "$prerelease_tags" | awk -v prefix="$next-$channel." '
        index($0, prefix) == 1 {
            number = substr($0, length(prefix) + 1)
            if (number ~ /^(0|[1-9][0-9]*)$/ && number + 0 > max) max = number + 0
        }
        END { printf "%.0f\n", max + 1 }
    ')
    next="$next-$channel.$number"
fi

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

echo "tagging $next (latest stable was $latest)"
git tag -a "$next" -m "Release $next"
git push origin "$next"
echo "tagged and pushed $next — release.yml will build+publish it"
