#!/bin/sh
# Бампит SemVer от последнего стабильного тега и пушит vX.Y.Z[-alpha.N|-beta.N].
# --dry-run: только напечатать следующую версию (без git tag/push).
set -e

usage() {
    echo "usage: $0 [--dry-run] {patch|minor|major} [alpha|beta] [--branch BRANCH]" >&2
    exit 2
}

if [ "${1:-}" = "--dry-run" ]; then dry=1; shift; else dry=0; fi
[ "$#" -ge 1 ] || usage
level="$1"
shift
channel=""
branch=""
explicit_branch=0
case "$level" in
    patch|minor|major) ;;
    *) usage ;;
esac
if [ "${1:-}" = alpha ] || [ "${1:-}" = beta ]; then channel="$1"; shift; fi
if [ "${1:-}" = --branch ]; then
    [ "$#" -eq 2 ] && [ -n "$2" ] || usage
    branch="$2"
    explicit_branch=1
    shift 2
fi
[ "$#" -eq 0 ] || usage

# Явная ветка релизится по запушенному SHA, независимо от checkout вызывающего.
target=$(git rev-parse HEAD)
if [ -n "$branch" ]; then
    git check-ref-format "refs/heads/$branch" >/dev/null || usage
    git fetch --quiet origin --tags "+refs/heads/$branch:refs/remotes/origin/$branch"
    target=$(git rev-parse "refs/remotes/origin/$branch^{commit}")
else
    branch=$(git symbolic-ref --quiet --short HEAD || true)
fi
line_major=$(printf '%s\n' "$branch" | sed -n 's/^v\([1-9][0-9]*\)\.x$/\1/p')
if [ -n "$line_major" ] && [ "$level" = major ]; then
    echo "error: major releases are not allowed on $branch" >&2
    exit 1
fi

# Только стабильные SemVer-теги, достижимые из выбранного SHA: prerelease не меняет базу
# следующего бампа.
stable_tags=$(git tag --list --merged "$target" --sort=-version:refname)
latest=$(printf '%s\n' "$stable_tags" | awk '
    /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/ { print; exit }
')
[ -n "$latest" ] || latest=v0.0.0
version=${latest#v}
major=${version%%.*}
minor_patch=${version#*.}
minor=${minor_patch%%.*}
patch=${minor_patch#*.}

if [ -n "$line_major" ] && [ "$major" != "$line_major" ]; then
    echo "error: $branch requires stable v$line_major tags (found $latest)" >&2
    exit 1
fi

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
if [ "$explicit_branch" = 0 ] && [ -n "$(git status --porcelain)" ]; then
    echo "error: working tree not clean — commit/stash first" >&2
    exit 1
fi
# предохранитель от повторного тега
if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
    echo "error: tag $next already exists" >&2
    exit 1
fi

echo "tagging $next (latest stable was $latest)"
git tag -a "$next" "$target" -m "Release $next"
git push origin "$next"
echo "tagged and pushed $next — release.yml will build+publish it"
