#!/bin/sh
set -e

# Entrypoint drops root to host UID/GID so mounted files retain host ownership.

# Хостовый uid/gid передаёт afm (ReExec) через -e AFM_HOST_UID/GID. Если скрипт
# запускают без них (например, образ вслепую), по умолчанию берём 1000:1000.
: "${AFM_HOST_UID:=1000}"
: "${AFM_HOST_GID:=1000}"

# Дом для non-root пользователя. Chown только сам каталог (без -R): в рекурсию попали бы примонтированные
# подкаталоги хоста — медленно и лишние записи.
mkdir -p /home/afm
chown "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm

# Codex OAuth-состояние: смонтировано read-only в /tmp/host-codex, здесь копируется в $HOME/.codex — codex
# может обновлять auth.json внутри контейнера, не задевая хостовый ~/.codex.
if [ -d /tmp/host-codex ]; then
  mkdir -p /home/afm/.codex
  cp -a /tmp/host-codex/. /home/afm/.codex/
  chown -R "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm/.codex
fi

# Задаём HOME после gosu: для UID вне /etc/passwd gosu иначе установит HOME=/.
exec gosu "$AFM_HOST_UID:$AFM_HOST_GID" env HOME=/home/afm PATH="/home/afm/.local/bin:$PATH" /usr/local/bin/afm "$@"
