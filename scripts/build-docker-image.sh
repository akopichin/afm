#!/bin/sh
set -eu

release_tag=${1:?Usage: build-docker-image.sh VERSION BINARY_DIRECTORY [--push]}
binaries=${2:?Supply prebuilt linux_amd64/afm and linux_arm64/afm}
mode=${3:---load}
case "$mode" in --push|--load) ;; *) exit 2 ;; esac
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
context=$(mktemp -d)
trap 'find "$context" -type f -delete; find "$context" -depth -type d -empty -delete' EXIT HUP INT TERM
mkdir -p "$context/binaries/linux_amd64" "$context/binaries/linux_arm64" "$context/scripts"
cp "$binaries/linux_amd64/afm" "$context/binaries/linux_amd64/afm"
cp "$binaries/linux_arm64/afm" "$context/binaries/linux_arm64/afm"
cp "$root/Dockerfile.runtime" "$root/docker-entrypoint.sh" "$context/"
cp "$root"/contracts/adapters/*-as-claude.sh "$root/contracts/adapters/install-cursor-agent.sh" "$root/scripts/telegram-notify.sh" "$context/scripts/"
set -- -t "akopichin/afm:$release_tag"
case "$release_tag" in
  *-*) ;;
  v1.*) set -- "$@" -t akopichin/afm:1 ;;
  v2.*) set -- "$@" -t akopichin/afm:2 ;;
  v*) major=${release_tag#v}; major=${major%%.*}; set -- "$@" -t "akopichin/afm:$major" -t akopichin/afm:latest ;;
esac
platforms=linux/amd64,linux/arm64
if [ "$mode" = --load ]; then platforms=${AFM_DOCKER_PLATFORM:-linux/arm64}; fi
docker buildx build --platform "$platforms" --build-arg "AFM_VERSION=$release_tag" \
  -f "$context/Dockerfile.runtime" "$@" "$mode" "$context"
