#!/bin/sh
# Версионированный релиз docker-имиджа: бампит SemVer от последнего git-тега,
# собирает :vX.Y.Z + :latest, пушит оба, и только после успеха создаёт локальный тег.
# --dry-run: только напечатать следующую версию (без build/push/tag).
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

echo "releasing $next (latest was ${latest:-none})"
# Мультиарх-релиз: buildx собирает под amd64+arm64 и пушит манифест-лист одним
# шагом. Обычный `docker build` на arm64-хосте даёт single-arch образ → у тех, кто
# делает FROM akopichin/afm на amd64, сборка падает "no match for platform in
# manifest: not found". Раздельный `docker push` для мультиарха не подходит: образы
# не грузятся в локальный daemon, манифест-лист публикует сам buildx через --push.
# docker-container-драйвер обязателен (драйвер `docker` не умеет манифест-листы) —
# создаём именованный билдер идемпотентно, если его ещё нет.
PLATFORMS="linux/amd64,linux/arm64"
BUILDER="afm-multiarch"
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
    docker buildx create --name "$BUILDER" --driver docker-container --bootstrap >/dev/null
fi
docker buildx build --builder "$BUILDER" \
    --platform "$PLATFORMS" \
    --build-arg AFM_VERSION="$next" \
    -t akopichin/afm:"$next" -t akopichin/afm:latest \
    -f Dockerfile.runtime \
    --push .

# git-тег — только после успешного пуша образа (орфан-тег при сбое пуша исключён).
git tag -a "$next" -m "Release $next"

# Пушим git-тег в remote автоматически. Образ уже опубликован, поэтому сбой пуша
# тега (нет сети/auth) НЕ валит релиз — предупреждаем и оставляем пушить вручную.
if git push origin "$next"; then
    echo "released $next (image + git tag pushed)"
else
    echo "released $next (image pushed; git tag $next is LOCAL — push manually: git push origin $next)" >&2
fi
